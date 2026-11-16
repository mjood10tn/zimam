<?php

namespace Tests\Feature\Deployments;

use App\Agent\Exceptions\AgentValidationFailed;
use App\Filament\Resources\SiteResource\Pages\DeploymentsTab;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteRepository;
use Illuminate\Support\Facades\DB;
use Livewire\Features\SupportTesting\Testable;
use Livewire\Livewire;
use Opis\JsonSchema\Validator;

/** Connecting a site to a GitHub repository (R02-D4, R02-D11): site.repo, the deploy key, the webhook secret and deploy URL. */
final class RepositoryTest extends DeploymentsTestCase
{
    public function testConnectRepositorySubmitsSiteRepo(): void
    {
        $site = $this->site();

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['repository' => 'octo/shop', 'branch' => 'main'], 'repositoryForm')
            ->call('connect')
            ->assertHasNoFormErrors([], 'repositoryForm')
            ->assertNotified(__('zimam.deployments.repository.submitted', ['name' => 'shop']));

        $this->assertSame([['name' => 'shop', 'repository' => 'octo/shop', 'branch' => 'main']], $this->submitted('site.repo'));
        $job = AgentJob::sole();
        $this->assertSame('site.repo', $job->command);
        $this->assertTrue($job->related->is($site));
        $this->assertSame(600, $job->runtime_max_seconds);
        $repository = SiteRepository::sole();
        $this->assertTrue($repository->site->is($site));
        $this->assertSame('github', $repository->provider);
        $this->assertSame('octo/shop', $repository->repository);
        $this->assertSame('main', $repository->branch);
        $this->assertNull($repository->deploy_key_public);
        $this->assertSame(5, $repository->keep_releases);
        $this->assertFalse($repository->auto_deploy);
        $this->assertTrue($repository->run_composer);
    }

    /** A new key is asked for only when the owner says so (R02-D4). */
    public function testReconnectCanRotateKey(): void
    {
        $site = $this->site();
        $this->connected($site);

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertFormSet(['repository' => 'octo/shop', 'branch' => 'main'], 'repositoryForm')
            ->fillForm(['repository' => 'octo/store', 'branch' => 'release/2.x', 'rotate_key' => true], 'repositoryForm')
            ->call('connect')
            ->assertHasNoFormErrors([], 'repositoryForm');

        $this->assertSame([['name' => 'shop', 'repository' => 'octo/store', 'branch' => 'release/2.x', 'rotate_key' => true]], $this->submitted('site.repo'));
        // The stored repository changes once the agent has made the switch.
        $this->assertSame('octo/shop', SiteRepository::sole()->repository);
        $this->trackToTheEnd(AgentJob::sole());
        $this->assertSame('octo/store', SiteRepository::sole()->repository);
        $this->assertSame('release/2.x', SiteRepository::sole()->branch);
    }

    public function testDeployKeyShownAfterOutcome(): void
    {
        $site = $this->site();
        $tab = Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['repository' => 'octo/shop', 'branch' => 'main'], 'repositoryForm')
            ->call('connect');

        Livewire::test(DeploymentsTab::class, ['site' => $site->fresh()])
            ->assertSee(__('zimam.deployments.key.pending'))
            ->assertDontSee('ssh-ed25519');

        $this->trackToTheEnd(AgentJob::sole());

        $repository = SiteRepository::sole();
        $this->assertMatchesRegularExpression('/^ssh-ed25519 [A-Za-z0-9+\/]+=*\z/', $repository->deploy_key_public);
        $this->assertMatchesRegularExpression('/^SHA256:[A-Za-z0-9+\/]{43}\z/', $repository->deploy_key_fingerprint);
        Livewire::test(DeploymentsTab::class, ['site' => $site->fresh()])
            ->assertSee($repository->deploy_key_public)
            ->assertSee($repository->deploy_key_fingerprint)
            ->assertSee(__('zimam.deployments.key.hint'))
            ->assertDontSee(__('zimam.deployments.key.pending'));
        unset($tab);
    }

    /** A failed site.repo leaves the reason on the site and no key. */
    public function testFailedConnectKeepsNoKey(): void
    {
        $site = $this->site();
        $this->agent->scriptJob('site.repo', ['running', 'failed'], message: 'apt-get failed');
        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['repository' => 'octo/shop', 'branch' => 'main'], 'repositoryForm')
            ->call('connect');

        $this->trackToTheEnd(AgentJob::sole());

        $this->assertNull(SiteRepository::sole()->deploy_key_public);
        $this->assertSame('apt-get failed', $site->fresh()->last_error);
    }

    /** The agent's field errors land on the form, and a first connect leaves no row behind. */
    public function testAgentRefusalLeavesNothing(): void
    {
        $site = $this->site();
        $this->agent->respond('site.repo', new AgentValidationFailed('Invalid parameters', ['branch' => 'is not on GitHub']));

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['repository' => 'octo/shop', 'branch' => 'main'], 'repositoryForm')
            ->call('connect')
            ->assertHasFormErrors(['branch'], 'repositoryForm');

        $this->assertSame(0, SiteRepository::count());
        $this->assertSame(0, AgentJob::count());
    }

    /** The deploy token in the deploy URL the tab shows right now. */
    private static function shownToken(Testable $tab): ?string
    {
        return preg_match('#/hooks/deploy/[a-z0-9-]+/([0-9a-f]{64})#', $tab->html(), $m) === 1 ? $m[1] : null;
    }

    /**
     * A revealed secret is rendered in the response that made it and nowhere
     * else: never in the component's state or snapshot, which ride along on
     * every later request, and not in the next response.
     */
    private function assertShownOnlyOnce(Testable $tab, string $value): void
    {
        $this->assertStringContainsString($value, $tab->html());
        $this->assertStringNotContainsString($value, json_encode($tab->snapshot));
        $tab->call('$refresh');
        $this->assertStringNotContainsString($value, $tab->html());
        $this->assertStringNotContainsString($value, json_encode($tab->snapshot));
        $this->assertStringNotContainsString($value, json_encode($tab->instance()->all()));
    }

    public function testWebhookSecretShownOnce(): void
    {
        $site = $this->site();
        $tab = Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['repository' => 'octo/shop', 'branch' => 'main'], 'repositoryForm')
            ->call('connect');

        $repository = SiteRepository::sole();
        $secret = $repository->webhook_secret;
        $this->assertMatchesRegularExpression('/^[0-9a-f]{64}\z/', $secret);
        $token = self::shownToken($tab);
        $this->assertNotNull($token);
        $this->assertTrue($repository->matchesDeployToken($token));
        $tab->assertSee($secret)
            ->assertSee($repository->webhookUrl())
            ->assertSee($repository->deployUrl($token))
            ->assertSee(__('zimam.deployments.webhook.setup', ['url' => $repository->webhookUrl()]));

        // Stored encrypted, the token only as its SHA-256, and neither in the model's array.
        $row = DB::table('site_repositories')->sole();
        $this->assertStringNotContainsString($secret, $row->webhook_secret);
        $this->assertSame(hash('sha256', $token), $row->deploy_token_hash);
        $this->assertArrayNotHasKey('webhook_secret', $repository->toArray());
        $this->assertArrayNotHasKey('deploy_token_hash', $repository->toArray());

        $this->assertShownOnlyOnce($tab, $secret);
        $this->assertNull(self::shownToken($tab));
        $this->assertStringNotContainsString($token, json_encode($tab->snapshot));
        $tab->assertSee(__('zimam.deployments.webhook.hidden'))->assertSee(__('zimam.deployments.deploy_url.hidden'));

        Livewire::test(DeploymentsTab::class, ['site' => $site->fresh()])
            ->assertDontSee($secret)
            ->assertDontSee($token)
            ->assertSee($repository->webhookUrl())
            ->assertSee(__('zimam.deployments.webhook.hidden'));
    }

    public function testRegenerateWebhookSecretNeedsPassword(): void
    {
        $site = $this->site();
        $repository = $this->connected($site);
        $old = $repository->webhook_secret;

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->callAction('regenerateWebhookSecret', ['current_password' => 'not the password'])
            ->assertHasActionErrors(['current_password']);
        $this->assertSame($old, $repository->fresh()->webhook_secret);

        $tab = Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->callAction('regenerateWebhookSecret', ['current_password' => self::PASSWORD])
            ->assertHasNoActionErrors();
        $new = $repository->fresh()->webhook_secret;
        $this->assertNotSame($old, $new);
        $this->assertShownOnlyOnce($tab, $new);
    }

    public function testRegenerateDeployUrlInvalidatesOldToken(): void
    {
        $site = $this->site();
        $tab = Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['repository' => 'octo/shop', 'branch' => 'main'], 'repositoryForm')
            ->call('connect');
        $old = self::shownToken($tab);
        $this->assertTrue(SiteRepository::sole()->matchesDeployToken($old));
        $this->assertShownOnlyOnce($tab, $old);

        Livewire::test(DeploymentsTab::class, ['site' => $site->fresh()])
            ->callAction('regenerateDeployUrl', ['current_password' => 'not the password'])
            ->assertHasActionErrors(['current_password']);
        $this->assertTrue(SiteRepository::sole()->matchesDeployToken($old));

        $tab = Livewire::test(DeploymentsTab::class, ['site' => $site->fresh()])
            ->callAction('regenerateDeployUrl', ['current_password' => self::PASSWORD])
            ->assertHasNoActionErrors();
        $new = self::shownToken($tab);
        $this->assertMatchesRegularExpression('/^[0-9a-f]{64}\z/', (string) $new);
        $repository = SiteRepository::sole();
        $this->assertFalse($repository->matchesDeployToken($old));
        $this->assertTrue($repository->matchesDeployToken($new));
        $tab->assertSee($repository->deployUrl($new));
        $this->assertShownOnlyOnce($tab, $new);
    }

    /**
     * The form's checks are the agent's (global rule 9): the same patterns,
     * anchored with \z, agree with the generated schema on every value.
     */
    public function testRepositoryValidationMatchesAgent(): void
    {
        $schema = json_decode(file_get_contents(base_path('../contract/schema/site.repo.params.json')));
        $valid = fn (string $field, string $value): bool => (new Validator)->validate(
            json_decode(json_encode(['name' => 'shop', 'repository' => 'octo/shop', 'branch' => 'main', $field => $value])),
            $schema,
        )->isValid();
        $repositories = ['octo/shop', 'Octo-Cat/shop.web_1', 'a/b', 'octo/.github', 'octo/app.GIT', 'octo/app.git', 'octo/app.Git', 'octo/.', 'octo/..', '-octo/shop', 'octo', 'octo/shop/x', 'octo/sh op', "octo/shop\n", 'octo/', str_repeat('a', 40).'/x', str_repeat('a', 39).'/x', 'octo/'.str_repeat('b', 101)];
        foreach ($repositories as $value) {
            $this->assertSame($valid('repository', $value), SiteRepository::isValidRepository($value), 'repository '.json_encode($value));
        }
        $branches = ['main', 'release/2.x', 'feature/a-b_c', 'v1.0', '-main', '/main', 'main/', 'main.', 'a..b', 'a//b', '.hidden', 'a/.b', 'main.lock', 'a.lock/b', 'a.locked', 'ma in', 'a@{b', "main\n", '', str_repeat('b', 200), str_repeat('b', 201)];
        foreach ($branches as $value) {
            $this->assertSame($valid('branch', $value), SiteRepository::isValidBranch($value), 'branch '.json_encode($value));
        }

        $site = $this->site();
        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->fillForm(['repository' => 'octo/app.git', 'branch' => 'a..b'], 'repositoryForm')
            ->call('connect')
            ->assertHasFormErrors(['repository', 'branch'], 'repositoryForm');
        $this->assertSame([], $this->submitted('site.repo'));
    }

    public function testStaticSiteHasNoComposerToggle(): void
    {
        $site = $this->site(Site::STATIC, 'docs');
        $repository = $this->connected($site);
        $this->assertFalse($repository->run_composer);

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertFormFieldHidden('run_composer', 'settingsForm')
            ->assertFormFieldVisible('deploy_script', 'settingsForm');
    }
}
