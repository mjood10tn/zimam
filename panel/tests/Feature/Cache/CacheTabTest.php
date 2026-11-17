<?php

namespace Tests\Feature\Cache;

use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\Exceptions\AgentUnavailable;
use App\Filament\Pages\Cache;
use App\Filament\Resources\SiteResource\Pages\CacheTab;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Models\AgentAction;
use App\Models\AgentJob;
use App\Models\ServerService;
use App\Models\Site;
use App\Models\SiteCredential;
use Filament\Notifications\Notification;
use Illuminate\Log\Events\MessageLogged;
use Illuminate\Support\Facades\Cache as CacheStore;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Event;
use Illuminate\Support\Facades\Schema;
use Livewire\Features\SupportLockedProperties\CannotUpdateLockedPropertyException;
use Livewire\Features\SupportTesting\Testable;
use Livewire\Livewire;
use PHPUnit\Framework\Attributes\DataProvider;
use Tests\Feature\Databases\DatabasesTestCase;

/**
 * A site's Cache tab: its Valkey user, a new password shown once, and the
 * connection written to .env. The password is never kept.
 */
final class CacheTabTest extends DatabasesTestCase
{
    // The queue's table is scanned for the password, so jobs really queue.
    protected bool $fakeQueue = false;

    private function tab(Site $site): Testable
    {
        return Livewire::test(CacheTab::class, ['site' => $site]);
    }

    private function rotate(Testable $tab): string
    {
        $tab->callAction('rotate', ['current_password' => self::PASSWORD]);

        return $this->submitted('cache.user')[0]['password'];
    }

    private function assertNotInState(Testable $tab, string $value): void
    {
        $this->assertStringNotContainsString($value, json_encode($tab->snapshot));
        $this->assertStringNotContainsString($value, json_encode($tab->instance()->all()));
    }

    /** A rotated password whose job has succeeded, ready for writeEnv(). */
    private function rotated(Site $site): array
    {
        $tab = $this->tab($site);
        $password = $this->rotate($tab);
        $this->trackToTheEnd(AgentJob::sole());
        $tab->call('refreshRotation');

        return [$tab, $password];
    }

    public function testCacheTabHiddenUntilValkeyInstalled(): void
    {
        $site = $this->site();

        $this->tab($site)
            ->assertSee(__('zimam.cache.tab.not_installed'))
            ->assertSeeHtml('href="'.Cache::getUrl().'"')
            ->assertDontSee(__('zimam.cache.rotate.label_first'))
            ->assertDontSee(__('zimam.cache.tab.limits_heading'));

        $this->agent->installValkey();
        $this->travel(11)->seconds();

        $this->tab($site)->assertDontSee(__('zimam.cache.tab.not_installed'))->assertSee(__('zimam.cache.rotate.label_first'));
    }

    /** With no status cached the tab asks the service row, as the Cache page does. */
    public function testInstalledFallsBackToTheServiceRowWhenNoStatusIsCached(): void
    {
        $site = $this->site();
        ServerService::create(['name' => ServerService::VALKEY, 'status' => ServerService::INSTALLED, 'version' => '9.0.4']);
        // Neither reachable nor cached: only the row is left to go by.
        $this->agent->respond('system.status', new AgentUnavailable('down'), new AgentUnavailable('down'));
        CacheStore::forget('zimam.system_status');

        $tab = $this->tab($site);

        $tab->assertDontSee(__('zimam.cache.tab.not_installed'))->assertSee(__('zimam.cache.tab.limits_heading'));
    }

    /** The status going stale or empty while a password is on screen must not replace its block. */
    public function testOpenPasswordSurvivesTheStatusGoingAway(): void
    {
        $this->agent->installValkey();
        $tab = $this->tab($this->site());
        $password = $this->rotate($tab);
        $this->agent->changeServer(function (array $status): array {
            $status['services']['valkey']['installed'] = false;

            return $status;
        });
        $this->travel(11)->seconds();

        $tab->call('refreshRotation')
            ->assertSet('rotationKey', fn ($key) => $key !== null)
            ->assertSee(__('zimam.cache.rotate.pending').'')
            ->assertDontSee(__('zimam.cache.tab.not_installed'));
        $this->assertStringContainsString('service-password-', $tab->html());
        $this->assertNotInState($tab, $password);
    }

    public function testTabOnTheSitePage(): void
    {
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->assertSee(__('zimam.sites.tabs.cache'))
            ->assertSeeLivewire(CacheTab::class);
    }

    public function testTabShowsTheUserAndTheKnownLimits(): void
    {
        $this->agent->installValkey();
        $site = $this->site();

        $this->tab($site)
            ->assertSee(['s_shop', __('zimam.cache.tab.user_never'), 's_shop:'])
            ->assertSee([__('zimam.cache.tab.limits_heading'), 'FLUSHDB', 'cache:clear', 'config:clear', 'SCAN', 'workers'])
            ->assertSee(__('zimam.cache.tab.env_note'));
    }

    public function testTabShowsWhenThePasswordWasLastChanged(): void
    {
        $this->agent->installValkey();
        $site = $this->site();
        SiteCredential::create(['site_id' => $site->id, 'kind' => SiteCredential::VALKEY, 'username' => 's_shop', 'rotated_at' => now()->subDay()]);
        // A MySQL credential of the same site doesn't count.
        SiteCredential::create(['site_id' => $site->id, 'kind' => SiteCredential::MYSQL, 'username' => 's_shop', 'rotated_at' => now()]);

        $this->tab($site)->assertDontSee(__('zimam.cache.tab.user_never'))->assertSee(__('zimam.cache.rotate.label'));
    }

    public function testRotateNeedsPasswordConfirmation(): void
    {
        $this->agent->installValkey();

        $this->tab($this->site())
            ->callAction('rotate', ['current_password' => 'wrong'])
            ->assertHasActionErrors(['current_password']);

        $this->assertSame([], $this->submitted('cache.user'));
    }

    public function testRotateCacheUserShowsPasswordOnce(): void
    {
        $logged = [];
        Event::listen(MessageLogged::class, function (MessageLogged $e) use (&$logged): void {
            $logged[] = $e->message.' '.json_encode($e->context, JSON_INVALID_UTF8_SUBSTITUTE);
        });
        $this->agent->installValkey();
        $site = $this->site();

        $tab = $this->tab($site)->callAction('rotate', ['current_password' => self::PASSWORD]);
        $password = $this->submitted('cache.user')[0]['password'];

        $this->assertSame(43, strlen($password));
        $this->assertMatchesRegularExpression('/^[A-Za-z0-9_-]{24,64}\z/', $password);
        $this->assertSame(['site' => 'shop', 'password' => $password], $this->submitted('cache.user')[0]);

        $tab->assertSeeHtml('value="'.$password.'"')->assertSeeHtml('wire:ignore')->assertSeeHtml('x-show="$wire.rotationDone"');
        $this->assertDoesNotMatchRegularExpression('/<input[^>]*\b(name|wire:model)=/', $tab->html());
        $this->assertNotInState($tab, $password);
        $this->assertSame(1, DB::table('jobs')->count(), 'the tracking job is queued in the database, without the command');
        $this->assertNoTableHolds($password);

        $job = AgentJob::sole();
        $this->assertSame(['site' => 'shop', 'password' => '[redacted]'], $job->params);
        $this->trackToTheEnd($job);
        $this->assertSame(AgentJob::SUCCEEDED, $job->status);

        $tab->call('refreshRotation')->assertSet('rotationDone', true)->assertDontSeeHtml($password)->assertSee(__('zimam.cache.rotate.ready'));
        $this->assertNotInState($tab, $password);
        $tab->call('$refresh')->assertDontSeeHtml($password);

        $credential = SiteCredential::sole();
        $this->assertSame(SiteCredential::VALKEY, $credential->kind);
        $this->assertSame('s_shop', $credential->username);
        $this->assertNotNull($credential->rotated_at);
        $this->assertNotContains('password', Schema::getColumnListing('site_credentials'));

        $this->assertSame(['site' => 'shop', 'password' => '[redacted]'], AgentAction::where('command', 'cache.user')->sole()->params);
        $this->assertNoTableHolds($password);
        foreach ($logged as $line) {
            $this->assertStringNotContainsString($password, $line);
        }
        $this->assertStringNotContainsString($password, json_encode(session()->all()));

        $tab->call('closeRotation')->assertSet('rotationKey', null)->assertDontSeeHtml($password);
        $this->assertNoTableHolds($password);
        $this->tab($site)->assertDontSeeHtml($password);
    }

    public function testRotationSaysRunningWorkersNeedARestart(): void
    {
        $this->agent->installValkey();
        $tab = $this->tab($this->site());
        $this->rotate($tab);

        $this->trackToTheEnd(AgentJob::sole());
        $tab->assertDontSee('restart the site’s running workers');
        $tab->call('refreshRotation')->assertSee(__('zimam.cache.rotate.ready'))->assertSee('restart the site’s running workers');
    }

    /** A second rotation of the same user makes a different password, and a user that exists keeps its row. */
    public function testEveryRotationMakesANewPassword(): void
    {
        $this->agent->installValkey();
        $tab = $this->tab($this->site());
        $first = $this->rotate($tab);
        $this->trackToTheEnd(AgentJob::sole());
        $tab->call('refreshRotation')->call('closeRotation');

        $tab->callAction('rotate', ['current_password' => self::PASSWORD]);

        $passwords = array_column($this->submitted('cache.user'), 'password');
        $this->assertCount(2, $passwords);
        $this->assertSame($first, $passwords[0]);
        $this->assertNotSame($first, $passwords[1]);
        $this->assertSame(1, SiteCredential::count());
    }

    /** A failed rotation leaves the old password working, and says so; the new one is discarded. */
    public function testFailedRotationDiscardsThePasswordAndSaysTheOldOneWorks(): void
    {
        $this->agent->installValkey();
        $site = $this->site();
        SiteCredential::create(['site_id' => $site->id, 'kind' => SiteCredential::VALKEY, 'username' => 's_shop', 'rotated_at' => now()->subDay()]);
        $this->agent->scriptJob('cache.user', ['running', 'failed'], errorCode: 'internal', message: 'ACL LOAD refused the file');
        $tab = $this->tab($site);
        $password = $this->rotate($tab);

        $this->trackToTheEnd(AgentJob::sole());
        $tab->call('refreshRotation')
            ->assertSet('rotationKey', null)
            ->assertDontSeeHtml($password)
            ->assertSee(__('zimam.cache.rotate.failed_kept', ['reason' => 'ACL LOAD refused the file']))
            ->assertSee(__('zimam.cache.rotate.label'));

        $this->assertNotInState($tab, $password);
        $this->assertNoTableHolds($password);
        $this->assertTrue(SiteCredential::sole()->rotated_at->isYesterday());
    }

    public function testFailedFirstPasswordSaysTheUserCannotLogInYet(): void
    {
        $this->agent->installValkey();
        $this->agent->scriptJob('cache.user', ['running', 'failed'], errorCode: 'internal', message: 'no');
        $tab = $this->tab($this->site());
        $password = $this->rotate($tab);

        $this->trackToTheEnd(AgentJob::sole());

        $tab->call('refreshRotation')->assertSee(__('zimam.cache.rotate.failed_new', ['reason' => 'no']));
        $this->assertSame(0, SiteCredential::count());
        $this->assertNoTableHolds($password);
    }

    public function testBusyRotationIsRefusedNotQueued(): void
    {
        $this->agent->installValkey();
        $this->agent->respond('cache.user', new AgentBusy('site:shop already has a job running'));

        $tab = $this->tab($this->site())->callAction('rotate', ['current_password' => self::PASSWORD]);

        $tab->assertNotified(__('zimam.cache.rotate.busy'))->assertSet('rotationKey', null);
        $this->assertSame(0, AgentJob::count());
        $this->assertSame(0, DB::table('jobs')->count());
        $password = $this->submitted('cache.user')[0]['password'];
        $this->assertNoTableHolds($password);
        $tab->assertDontSeeHtml($password);
    }

    /** The agent can refuse a rotation (Valkey down): its words are shown and nothing is kept. */
    public function testRefusedRotationShowsTheAgentsWords(): void
    {
        $this->agent->installValkey();
        $this->agent->respond('cache.user', new AgentConflict('Valkey isn’t answering. Start it and try again.'));

        $tab = $this->tab($this->site())->callAction('rotate', ['current_password' => self::PASSWORD]);

        $tab->assertNotified(Notification::make()->title(__('zimam.cache.rotate.refused'))->body('Valkey isn’t answering. Start it and try again.')->danger())
            ->assertSet('rotationKey', null);
        $this->assertNoTableHolds($this->submitted('cache.user')[0]['password']);
    }

    public function testWriteToEnvSetsRedisKeysAndPrefix(): void
    {
        $this->agent->installValkey();
        $site = $this->site();
        $this->agent->env('shop', "# Shop\nAPP_NAME=Shop\nREDIS_HOST=localhost\nCACHE_STORE=file\nQUEUE_CONNECTION=sync\nSESSION_DRIVER=file\nREDIS_PREFIX=old_\n");
        [$tab, $password] = $this->rotated($site);

        $tab->call('writeEnv', $password)
            ->assertSet('envWritten', true)
            ->assertNotified(__('zimam.cache.write_env.submitted', ['name' => 'shop']));

        $write = AgentJob::where('command', 'site.env.write')->sole();
        $this->assertSame(['name' => 'shop', 'content' => '[redacted]'], $write->params);
        $this->trackToTheEnd($write);
        // The site keeps choosing its own cache, queue and session drivers.
        $this->assertSame(
            "# Shop\nAPP_NAME=Shop\nREDIS_HOST=127.0.0.1\nCACHE_STORE=file\nQUEUE_CONNECTION=sync\nSESSION_DRIVER=file\nREDIS_PREFIX=s_shop:\nREDIS_CLIENT=phpredis\nREDIS_PORT=6379\nREDIS_USERNAME=s_shop\nREDIS_PASSWORD={$password}\n",
            $this->agent->envOf('shop'),
        );
        $this->assertNotInState($tab, $password);
        $this->assertNoTableHolds($password);
        $this->assertSame(['name' => 'shop', 'content' => '[redacted]'], AgentAction::where('command', 'site.env.write')->sole()->params);
    }

    public function testWriteToEnvMakesAMissingEnv(): void
    {
        $this->agent->installValkey();
        [$tab, $password] = $this->rotated($this->site('blog'));

        $tab->call('writeEnv', $password)->assertSet('envWritten', true);
        $this->trackToTheEnd(AgentJob::where('command', 'site.env.write')->sole());

        $this->assertSame("REDIS_CLIENT=phpredis\nREDIS_HOST=127.0.0.1\nREDIS_PORT=6379\nREDIS_USERNAME=s_blog\nREDIS_PASSWORD={$password}\nREDIS_PREFIX=s_blog:\n", $this->agent->envOf('blog'));
    }

    public function testWriteToEnvNeedsASucceededRotation(): void
    {
        $this->agent->installValkey();
        $site = $this->site();

        $this->tab($site)->call('writeEnv', str_repeat('a', 43));
        $tab = $this->tab($site);
        $password = $this->rotate($tab);
        $tab->call('writeEnv', $password);

        $this->assertSame([], $this->submitted('site.env.write'));
        $this->assertSame([], $this->submitted('site.env.read'));
    }

    public function testWriteToEnvRefusesAPasswordThatIsNotTheOneMade(): void
    {
        $this->agent->installValkey();
        [$tab, $password] = $this->rotated($this->site());
        $forged = str_repeat('x', 43);

        $tab->call('writeEnv', $forged)->assertNotified(__('zimam.cache.write_env.mismatch'))->assertSet('envWritten', false);
        $tab->call('writeEnv', "short\n")->call('writeEnv', ['array']);
        $this->assertSame([], $this->submitted('site.env.write'));

        $tab->call('writeEnv', $password)->assertSet('envWritten', true);
    }

    public function testWriteToEnvExpiresWithThePasswordConfirmation(): void
    {
        $this->agent->installValkey();
        [$tab, $password] = $this->rotated($this->site());

        $this->travel((int) config('auth.password_timeout') + 1)->seconds();
        $tab->call('writeEnv', $password)
            ->assertNotified(__('zimam.cache.write_env.expired'))
            ->assertSet('rotationKey', null);

        $this->assertSame([], $this->submitted('site.env.write'));
    }

    public function testBusyEnvWriteIsRefusedNotQueued(): void
    {
        $this->agent->installValkey();
        [$tab, $password] = $this->rotated($this->site());
        $this->agent->respond('site.env.write', new AgentBusy('site:shop already has a job running'));

        $tab->call('writeEnv', $password)->assertNotified(__('zimam.cache.rotate.busy'))->assertSet('envWritten', false);

        $this->assertSame(0, AgentJob::where('command', 'site.env.write')->count());
        $this->assertNoTableHolds($password);
    }

    /** The same checks as the Environment tab, before anything is read. */
    #[DataProvider('blockedSites')]
    public function testWriteToEnvIsRefusedWhileTheSiteCannotTakeAJob(string $why): void
    {
        $this->agent->installValkey();
        $site = $this->site();
        [$tab, $password] = $this->rotated($site);

        match ($why) {
            'deleting' => $site->update(['status' => Site::DELETING]),
            'waiting' => $site->jobs()->create(['command' => 'site.deploy', 'status' => AgentJob::WAITING, 'params' => []]),
        };
        $tab->call('writeEnv', $password)->assertNotified(__('zimam.cache.write_env.refused'));

        $this->assertSame([], $this->submitted('site.env.read'));
        $this->assertSame([], $this->submitted('site.env.write'));
        $this->assertNoTableHolds($password);
    }

    /** @return array<string, array{string}> */
    public static function blockedSites(): array
    {
        return ['a site being deleted' => ['deleting'], 'a job waiting for the agent' => ['waiting']];
    }

    public function testRotateSaysWhyTheSiteCannotTakeItNow(): void
    {
        $this->agent->installValkey();
        foreach ([Site::DELETING, Site::FAILED, Site::PROVISIONING] as $i => $status) {
            $site = $this->site("site{$i}");
            $site->update(['status' => $status]);

            $this->tab($site)->assertActionDisabled('rotate');
        }
        $this->assertSame([], $this->submitted('cache.user'));
    }

    /** An .env with an unterminated quote is not touched, and the message names the key and line. */
    public function testWriteToEnvRefusesAnUnterminatedQuote(): void
    {
        $this->agent->installValkey();
        $this->agent->env('shop', "A=1\nREDIS_PASSWORD=\"old\nAPP_KEY=base64:k\n");
        [$tab, $password] = $this->rotated($this->site());

        $tab->call('writeEnv', $password)
            ->assertNotified(Notification::make()->title(__('zimam.cache.write_env.refused'))->body(__('zimam.databases.write_env.malformed', ['key' => 'REDIS_PASSWORD', 'line' => 2]))->danger())
            ->assertSet('envWritten', false);

        $this->assertSame([], $this->submitted('site.env.write'));
        $this->assertSame("A=1\nREDIS_PASSWORD=\"old\nAPP_KEY=base64:k\n", $this->agent->envOf('shop'));
    }

    public function testWriteToEnvLetsBusyAndUnavailableReadsSayTheirOwnThing(): void
    {
        $this->agent->installValkey();
        [$tab, $password] = $this->rotated($this->site());

        $this->agent->respond('site.env.read', new AgentBusy('busy'));
        $tab->call('writeEnv', $password)->assertNotified(__('zimam.cache.rotate.busy'));

        $this->agent->respond('site.env.read', new AgentUnavailable('down'));
        $tab->call('writeEnv', $password)->assertNotified(__('zimam.agent.paused'));

        $this->assertSame([], $this->submitted('site.env.write'));
    }

    /** @return array<string, array{string, mixed}> */
    public static function lockedProperties(): array
    {
        return [
            'site' => ['site', null],
            'rotationJobId' => ['rotationJobId', 1],
            'rotationKey' => ['rotationKey', 'forged'],
            'rotationDone' => ['rotationDone', true],
            'hadUser' => ['hadUser', true],
            'rotationFailure' => ['rotationFailure', 'forged'],
            'confirmedAt' => ['confirmedAt', 9999999999],
            'envWritten' => ['envWritten', true],
            'passwordProof' => ['passwordProof', 'forged'],
        ];
    }

    #[DataProvider('lockedProperties')]
    public function testStateCannotBeSetFromTheBrowser(string $property, mixed $value): void
    {
        $this->expectException(CannotUpdateLockedPropertyException::class);

        $this->tab($this->site())->set($property, $value);
    }

    /** The tab is lazy: until it is opened, nothing asks the agent about Valkey. */
    public function testSitePageLoadMakesNoCacheCalls(): void
    {
        $this->agent->installValkey();
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->assertSee(__('zimam.sites.tabs.cache'));

        $this->assertSame([], $this->submitted('system.status'));
        $this->assertSame([], $this->submitted('cache.user'));
    }
}
