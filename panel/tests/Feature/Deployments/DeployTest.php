<?php

namespace Tests\Feature\Deployments;

use App\Actions\StartDeployment;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\JobOutcomes;
use App\Filament\Resources\SiteResource\Pages\DeploymentsTab;
use App\Models\AgentJob;
use App\Models\Deployment;
use App\Models\Site;
use App\Models\SiteRepository;
use Filament\Notifications\Notification;
use Livewire\Livewire;
use ReflectionMethod;

/** Deploy now, the deploy settings and the deployments list (R02-D2, R02-D5, R02-D8, R02-D12). */
final class DeployTest extends DeploymentsTestCase
{
    public function testDeployNowSubmitsDeploy(): void
    {
        $site = $this->site();
        $this->connected($site, ['deploy_script' => "php artisan migrate --force\n", 'run_composer' => true, 'keep_releases' => 7]);

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->call('deployNow')
            ->assertNotified(__('zimam.deployments.deploy.submitted', ['name' => 'shop']));

        $this->assertEquals([[
            'name' => 'shop',
            'composer' => true,
            'trigger' => 'manual',
            'keep_releases' => 7,
            'script' => "php artisan migrate --force\n",
        ]], $this->submitted('site.deploy'));
        $job = AgentJob::sole();
        $this->assertSame('site.deploy', $job->command);
        $this->assertTrue($job->related->is($site));
        $this->assertSame(1800, $job->runtime_max_seconds);
        $deployment = Deployment::sole();
        $this->assertTrue($deployment->site->is($site));
        $this->assertTrue($deployment->agentJob->is($job));
        $this->assertSame('manual', $deployment->trigger);
        $this->assertSame(Deployment::QUEUED, $deployment->status);
        $this->assertSame($this->user->id, $deployment->created_by);
        $this->assertNull($deployment->commit);
    }

    /** An empty script and a commit: the script is left out (it has no null), the commit sent. */
    public function testStartDeploymentWithCommitAndNoScript(): void
    {
        $site = $this->site(Site::STATIC, 'docs');
        $this->connected($site);
        $commit = str_repeat('ab', 20);

        $deployment = app(StartDeployment::class)->run($site, Deployment::WEBHOOK, $commit);

        $this->assertSame([['name' => 'docs', 'composer' => false, 'trigger' => 'webhook', 'keep_releases' => 5, 'commit' => $commit]], $this->submitted('site.deploy'));
        $this->assertSame($commit, $deployment->commit);
        $this->assertNull($deployment->created_by);
        $this->assertSame(Deployment::WEBHOOK, $deployment->trigger);
    }

    public function testLaravelPresetFillsScript(): void
    {
        $site = $this->site(Site::LARAVEL, 'blog');
        $repository = SiteRepository::createFor($site, 'octo/blog', 'main');
        // A new Laravel site starts from the preset (R02-D8).
        $this->assertSame(SiteRepository::LARAVEL_PRESET, $repository->deploy_script);
        $this->assertTrue($repository->run_composer);
        $repository->update(['deploy_script' => 'echo custom', 'run_composer' => false]);

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertFormSet(['deploy_script' => 'echo custom', 'run_composer' => false], 'settingsForm')
            ->call('applyLaravelPreset')
            ->assertFormSet(['deploy_script' => SiteRepository::LARAVEL_PRESET, 'run_composer' => true], 'settingsForm')
            ->call('saveSettings')
            ->assertHasNoFormErrors([], 'settingsForm')
            ->assertActionMounted('confirmDeployScript')
            ->setActionData(['current_password' => self::PASSWORD])
            ->callMountedAction();

        $this->assertSame(SiteRepository::LARAVEL_PRESET, $repository->fresh()->deploy_script);
        $this->assertTrue($repository->fresh()->run_composer);
        foreach (['php artisan storage:link', 'php artisan migrate --force', 'php artisan config:cache', 'php artisan route:cache', 'php artisan view:cache'] as $step) {
            $this->assertStringContainsString($step, SiteRepository::LARAVEL_PRESET);
        }
        $this->assertStringNotContainsString('composer', SiteRepository::LARAVEL_PRESET);
    }

    /** The script editor says how the script runs and what it can't do yet (R02-D2, OQ8). */
    public function testScriptHelpExplainsHowItRuns(): void
    {
        $site = $this->site();
        $this->connected($site);

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertSee(__('zimam.deployments.settings.script_help'))
            ->assertSee(__('zimam.deployments.settings.script_node'));
        $this->assertStringContainsString('stopped when the script ends', __('zimam.deployments.settings.script_help'));
        $this->assertStringContainsString('Node', __('zimam.deployments.settings.script_node'));
    }

    public function testSettingsSaved(): void
    {
        $site = $this->site();
        $repository = $this->connected($site);

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['auto_deploy' => true, 'deploy_script' => "echo one\necho two", 'run_composer' => false, 'keep_releases' => 20], 'settingsForm')
            ->call('saveSettings')
            ->assertHasNoFormErrors([], 'settingsForm')
            ->assertActionMounted('confirmDeployScript')
            ->setActionData(['current_password' => self::PASSWORD])
            ->callMountedAction()
            ->assertNotified(__('zimam.deployments.settings.saved'));

        $repository->refresh();
        $this->assertTrue($repository->auto_deploy);
        $this->assertSame("echo one\necho two", $repository->deploy_script);
        $this->assertFalse($repository->run_composer);
        $this->assertSame(20, $repository->keep_releases);
        // Saving settings asks nothing of the agent.
        $this->assertSame([], array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit'));
    }

    /** The script limit is 64 KiB in bytes, as the agent counts it, not in characters. */
    /**
     * The deploy script can read the site's .env, so changing it asks
     * for the password, as showing the .env does. Other settings don't.
     */
    public function testChangingTheScriptAsksForThePassword(): void
    {
        $site = $this->site();
        $repository = $this->connected($site, ['deploy_script' => 'echo before', 'keep_releases' => 5]);

        // Saving alone stores nothing, the other settings included.
        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['deploy_script' => 'cat .env', 'keep_releases' => 7], 'settingsForm')
            ->call('saveSettings')
            ->assertActionMounted('confirmDeployScript')
            ->assertNotNotified(__('zimam.deployments.settings.saved'));
        $this->assertSame('echo before', $repository->fresh()->deploy_script);
        $this->assertSame(5, $repository->fresh()->keep_releases);

        // Nor does the wrong password.
        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['deploy_script' => 'cat .env'], 'settingsForm')
            ->callAction('confirmDeployScript', ['current_password' => 'not the password'])
            ->assertHasActionErrors(['current_password']);
        $this->assertSame('echo before', $repository->fresh()->deploy_script);

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['deploy_script' => 'echo after', 'keep_releases' => 7], 'settingsForm')
            ->call('saveSettings')
            ->assertActionMounted('confirmDeployScript')
            ->setActionData(['current_password' => self::PASSWORD])
            ->callMountedAction()
            ->assertHasNoActionErrors();
        $this->assertSame('echo after', $repository->fresh()->deploy_script);
        $this->assertSame(7, $repository->fresh()->keep_releases);

        // With the script as it is, the rest saves at once.
        Livewire::test(DeploymentsTab::class, ['site' => $site->fresh()])
            ->fillForm(['auto_deploy' => true, 'keep_releases' => 9], 'settingsForm')
            ->call('saveSettings')
            ->assertHasNoFormErrors([], 'settingsForm');
        $this->assertSame(9, $repository->fresh()->keep_releases);
        $this->assertSame('echo after', $repository->fresh()->deploy_script);
        $this->assertStringContainsString('password', __('zimam.deployments.settings.script_help'));
    }

    /** The tab says what a deploy needs and how the script runs. */
    public function testHelpNamesThePublicFolderAndTheScriptsPhp(): void
    {
        $site = $this->site();
        $this->connected($site);

        Livewire::test(DeploymentsTab::class, ['site' => $site])->assertSee(__('zimam.deployments.help.repository'));
        $this->assertStringContainsString('public/', __('zimam.deployments.help.repository'));
        $this->assertStringContainsString('site’s PHP version', __('zimam.deployments.settings.script_help'));
        $this->assertStringContainsString('stdin', __('zimam.deployments.settings.script_help'));
    }

    public function testScriptLimitCountsBytes(): void
    {
        $site = $this->site();
        $repository = $this->connected($site);
        $save = fn (string $script) => Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['deploy_script' => $script], 'settingsForm')
            ->call('saveSettings');

        $save(str_repeat('a', 65536))->assertHasNoFormErrors([], 'settingsForm')
            ->assertActionMounted('confirmDeployScript')
            ->setActionData(['current_password' => self::PASSWORD])
            ->callMountedAction();
        $this->assertSame(65536, strlen($repository->fresh()->deploy_script));
        // 30000 characters, 90000 bytes.
        $save(str_repeat('€', 30000))->assertHasFormErrors(['deploy_script'], 'settingsForm');
        $save(str_repeat('a', 65537))->assertHasFormErrors(['deploy_script'], 'settingsForm');
        $save("echo a\0b")->assertHasFormErrors(['deploy_script'], 'settingsForm');
        $this->assertSame(65536, strlen($repository->fresh()->deploy_script));
        // Livewire's JSON can't carry bytes that aren't UTF-8, so the check is called directly.
        $this->assertSame(__('zimam.deployments.validation.script_utf8'), SiteRepository::scriptError("echo \xff"));
        $this->assertNull(SiteRepository::scriptError(str_repeat('€', 21845)));
    }

    public function testKeepReleasesRange(): void
    {
        $site = $this->site();
        $this->connected($site);

        foreach ([0, 21] as $keep) {
            Livewire::test(DeploymentsTab::class, ['site' => $site])
                ->fillForm(['keep_releases' => $keep], 'settingsForm')
                ->call('saveSettings')
                ->assertHasFormErrors(['keep_releases'], 'settingsForm');
        }
    }

    public function testDeploymentRowFollowsJob(): void
    {
        $site = $this->site();
        $this->connected($site);
        $deployment = app(StartDeployment::class)->run($site, Deployment::MANUAL, by: $this->user);
        $job = AgentJob::sole();
        $this->assertSame(Deployment::QUEUED, $deployment->status);

        $this->track($job);
        $deployment->refresh();
        $this->assertSame(Deployment::RUNNING, $deployment->status);
        $this->assertNotNull($deployment->started_at);
        $this->assertNull($deployment->finished_at);

        $this->trackToTheEnd($job);
        $deployment->refresh();
        $this->assertSame(Deployment::SUCCEEDED, $deployment->status);
        $this->assertSame($job->result['release'], $deployment->release);
        $this->assertSame($job->result['commit'], $deployment->commit);
        $this->assertMatchesRegularExpression('/^[0-9]{14}-[0-9a-f]{7}\z/', $deployment->release);
        $this->assertNotNull($deployment->finished_at);
        $this->assertNull($deployment->error_message);

        // A failed build: the reason is kept, and the live release stays (R02-D5).
        $this->agent->scriptJob('site.deploy', ['running', 'failed'], ['Running the deploy script', 'deploy script: exit 1'], errorCode: 'internal', message: 'The deploy script failed with exit code 1.');
        $failed = app(StartDeployment::class)->run($site, Deployment::MANUAL);
        $this->trackToTheEnd(AgentJob::latest('id')->first());
        $failed->refresh();
        $this->assertSame(Deployment::FAILED, $failed->status);
        $this->assertSame('The deploy script failed with exit code 1.', $failed->error_message);
        $this->assertNull($failed->release);
        $this->assertNull($site->fresh()->last_error);
    }

    /** A busy site waits and retries (R02-D12, D8); the row says waiting, then follows the job. */
    public function testBusyDeployWaits(): void
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->respond('site.deploy', new AgentBusy('site:shop already has a job running'));

        $deployment = app(StartDeployment::class)->run($site, Deployment::URL);

        $this->assertSame(Deployment::WAITING, $deployment->status);
        $job = AgentJob::sole();
        $this->assertSame(AgentJob::WAITING, $job->status);
        $job->markQueued(strtoupper((string) str()->ulid()));
        $this->assertSame(Deployment::QUEUED, $deployment->fresh()->status);
        app(JobOutcomes::class)->fail($job, 'busy', __('zimam.jobs.busy', ['minutes' => 35]));
        $this->assertSame(Deployment::FAILED, $deployment->fresh()->status);
        $this->assertSame(__('zimam.jobs.busy', ['minutes' => 35]), $deployment->fresh()->error_message);
    }

    public function testDeployListedWithLog(): void
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->scriptJob('site.deploy', ['running', 'succeeded'], ['Fetching octo/shop main', 'Making the release live'], result: ['name' => 'shop', 'release' => '20261116120000-abcdef1', 'commit' => 'abcdef1'.str_repeat('0', 33)]);
        app(StartDeployment::class)->run($site, Deployment::MANUAL, by: $this->user);
        $this->trackToTheEnd(AgentJob::sole());

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertSee('20261116120000-abcdef1')
            ->assertSee('abcdef1')
            ->assertSee(__('zimam.deployments.triggers.manual'))
            ->assertSee(__('zimam.jobs.status.succeeded'))
            ->assertSeeLivewire('job-log');
    }

    /** A deploy the agent refuses on its params names each field in the notification. */
    public function testDeployNowShowsAgentFieldErrors(): void
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->respond('site.deploy', new AgentValidationFailed('Invalid parameters', [
            'script' => 'must be at most 65536 bytes',
            'keep_releases' => 'must be between 1 and 20',
        ]));

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->call('deployNow')
            ->assertNotified(
                Notification::make()
                    ->title(__('zimam.deployments.deploy.refused'))
                    ->body("script: must be at most 65536 bytes\nkeep_releases: must be between 1 and 20")
                    ->danger(),
            );
        $this->assertSame(0, Deployment::count());
    }

    /** Only the tab's own methods are callable from the browser; its data readers aren't. */
    public function testDataReadersAreNotCallable(): void
    {
        foreach (['repository', 'releases', 'deployments'] as $method) {
            $this->assertFalse((new ReflectionMethod(DeploymentsTab::class, $method))->isPublic(), $method);
        }
    }

    /** Deploy now waits for a connected repository and for the site's other jobs. */
    public function testDeployNowNeedsConnectedIdleSite(): void
    {
        $site = $this->site();
        SiteRepository::createFor($site, 'octo/shop', 'main');

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->call('deployNow');
        $this->assertSame([], $this->submitted('site.deploy'));

        $this->connected($this->site(name: 'other'));
        $other = Site::where('name', 'other')->sole();
        $other->jobs()->create(['command' => 'site.security', 'status' => AgentJob::RUNNING, 'params' => [], 'agent_job_id' => strtoupper((string) str()->ulid())]);
        Livewire::test(DeploymentsTab::class, ['site' => $other])
            ->call('deployNow')
            ->assertNotified(__('zimam.sites.busy'));
        $this->assertSame([], $this->submitted('site.deploy'));
    }
}
