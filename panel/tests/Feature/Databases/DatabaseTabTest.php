<?php

namespace Tests\Feature\Databases;

use App\Agent\Exceptions\AgentBusy;
use App\Filament\Resources\SiteResource\Pages\DatabaseTab;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Models\AgentAction;
use App\Models\AgentJob;
use App\Models\ServerService;
use App\Models\Site;
use App\Models\SiteCredential;
use Illuminate\Log\Events\MessageLogged;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Event;
use Illuminate\Support\Facades\Schema;
use Livewire\Features\SupportLockedProperties\CannotUpdateLockedPropertyException;
use Livewire\Features\SupportTesting\Testable;
use Livewire\Livewire;
use PHPUnit\Framework\Attributes\DataProvider;

/**
 * A site's Database tab: its databases and MySQL user, a new password shown
 * once, and the connection written to .env. The password is never kept.
 */
final class DatabaseTabTest extends DatabasesTestCase
{
    // The queue's table is scanned for the password, so jobs really queue.
    protected bool $fakeQueue = false;

    private function tab(Site $site): Testable
    {
        return Livewire::test(DatabaseTab::class, ['site' => $site]);
    }

    /** MySQL is on the server and in the panel's rows, as after an install. */
    private function installed(): void
    {
        $this->agent->installMysql();
        ServerService::create(['name' => ServerService::MYSQL, 'status' => ServerService::INSTALLED, 'version' => '8.4.13']);
    }

    private function rotate(Testable $tab): string
    {
        $tab->callAction('rotate', ['current_password' => self::PASSWORD]);

        return $this->submitted('db.user')[0]['password'];
    }

    /** $value is in none of the component's state: its snapshot or its public properties. */
    private function assertNotInState(Testable $tab, string $value): void
    {
        $this->assertStringNotContainsString($value, json_encode($tab->snapshot));
        $this->assertStringNotContainsString($value, json_encode($tab->instance()->all()));
    }

    public function testTabShowsDatabasesAndTheUser(): void
    {
        $this->installed();
        $site = $this->site();
        $this->database($site, 'main', 1024);

        $this->tab($site)
            ->assertSee(['s_shop_main', '1 KiB', 's_shop', __('zimam.databases.tab.user_never')])
            ->assertSee(__('zimam.databases.rotate.label_first'));
    }

    public function testTabOnTheSitePage(): void
    {
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->assertSee(__('zimam.sites.tabs.database'))
            ->assertSeeLivewire(DatabaseTab::class);
    }

    public function testTabSaysMysqlIsNotInstalled(): void
    {
        $site = $this->site();

        $this->tab($site)
            ->assertSee(__('zimam.databases.not_installed'))
            ->assertActionDisabled('rotate');
    }

    public function testRotateNeedsPasswordConfirmation(): void
    {
        $this->installed();

        $this->tab($this->site())
            ->callAction('rotate', ['current_password' => 'not the password'])
            ->assertHasActionErrors(['current_password']);

        $this->assertSame([], $this->submitted('db.user'));
    }

    /** The password is in the response that made it and nowhere else: no state, row, log, session or queue payload. */
    public function testRotateShowsPasswordOnce(): void
    {
        $logged = [];
        Event::listen(MessageLogged::class, function (MessageLogged $e) use (&$logged): void {
            $logged[] = $e->message.' '.json_encode($e->context, JSON_INVALID_UTF8_SUBSTITUTE);
        });
        $this->installed();
        $site = $this->site();

        $tab = $this->tab($site)->callAction('rotate', ['current_password' => self::PASSWORD]);
        $password = $this->submitted('db.user')[0]['password'];

        // 32 random bytes as base64url without padding, in the agent's alphabet.
        $this->assertSame(43, strlen($password));
        $this->assertMatchesRegularExpression('/^[A-Za-z0-9_-]{24,64}\z/', $password);
        $this->assertSame(['site' => 'shop', 'password' => $password], $this->submitted('db.user')[0]);

        // Shown in this response, in a block the browser keeps and hides until the job has succeeded.
        $tab->assertSeeHtml('value="'.$password.'"')->assertSeeHtml('wire:ignore')->assertSeeHtml('x-show="$wire.rotationDone"');
        $this->assertDoesNotMatchRegularExpression('/<input[^>]*\b(name|wire:model)=/', $tab->html());
        $this->assertNotInState($tab, $password);
        $this->assertSame(1, DB::table('jobs')->count(), 'the tracking job is queued in the database, without the command');
        $this->assertNoTableHolds($password);

        $job = AgentJob::sole();
        $this->assertSame(['site' => 'shop', 'password' => '[redacted]'], $job->params);
        $this->trackToTheEnd($job);
        $this->assertSame(AgentJob::SUCCEEDED, $job->status);

        // Later requests, the poll included, carry none of it.
        $tab->call('refreshRotation')->assertSet('rotationDone', true)->assertDontSeeHtml($password)->assertSee(__('zimam.databases.rotate.ready'));
        $this->assertNotInState($tab, $password);
        $tab->call('$refresh')->assertDontSeeHtml($password);

        $credential = SiteCredential::sole();
        $this->assertSame('s_shop', $credential->username);
        $this->assertNotNull($credential->rotated_at);
        $this->assertNotContains('password', Schema::getColumnListing('site_credentials'));

        // The audit record has the command with the password redacted, and nothing else holds it.
        $this->assertSame(['site' => 'shop', 'password' => '[redacted]'], AgentAction::where('command', 'db.user')->sole()->params);
        $this->assertNoTableHolds($password);
        foreach ($logged as $line) {
            $this->assertStringNotContainsString($password, $line);
        }
        $this->assertStringNotContainsString($password, json_encode(session()->all()));

        // Closing it removes it for good: a fresh tab has none to show.
        $tab->call('closeRotation')->assertSet('rotationKey', null)->assertDontSeeHtml($password);
        $this->assertNoTableHolds($password);
        $this->tab($site)->assertDontSeeHtml($password);
    }

    /** Each rotation makes a different password. */
    public function testEveryRotationMakesANewPassword(): void
    {
        $this->installed();
        $site = $this->site();

        $first = $this->rotate($this->tab($site));
        $this->trackToTheEnd(AgentJob::sole());
        $this->tab($site)->callAction('rotate', ['current_password' => self::PASSWORD]);

        $passwords = array_column($this->submitted('db.user'), 'password');
        $this->assertCount(2, $passwords);
        $this->assertNotSame($first, $passwords[1]);
    }

    /** A failed rotation leaves the old password working, and says so; the new one is discarded. */
    public function testFailedRotationDiscardsThePasswordAndSaysTheOldOneWorks(): void
    {
        $this->installed();
        $site = $this->site();
        SiteCredential::create(['site_id' => $site->id, 'kind' => SiteCredential::MYSQL, 'username' => 's_shop', 'rotated_at' => now()->subDay()]);
        $this->agent->scriptJob('db.user', ['running', 'failed'], errorCode: 'internal', message: 'MySQL refused the ALTER USER');
        $tab = $this->tab($site);
        $password = $this->rotate($tab);

        $this->trackToTheEnd(AgentJob::sole());
        $tab->call('refreshRotation')
            ->assertSet('rotationKey', null)
            ->assertDontSeeHtml($password)
            ->assertSee(__('zimam.databases.rotate.failed_kept', ['reason' => 'MySQL refused the ALTER USER']))
            ->assertSee(__('zimam.databases.rotate.label'));

        $this->assertNotInState($tab, $password);
        $this->assertNoTableHolds($password);
        // The credential row still shows the last change that did succeed.
        $this->assertTrue(SiteCredential::sole()->rotated_at->isYesterday());
    }

    public function testFailedFirstPasswordSaysTheUserCannotLogInYet(): void
    {
        $this->installed();
        $this->agent->scriptJob('db.user', ['running', 'failed'], errorCode: 'internal', message: 'no');
        $tab = $this->tab($this->site());
        $this->rotate($tab);

        $this->trackToTheEnd(AgentJob::sole());

        $tab->call('refreshRotation')->assertSee(__('zimam.databases.rotate.failed_new', ['reason' => 'no']));
        $this->assertSame(0, SiteCredential::count());
    }

    /** A busy site is refused, not queued: a queued command would hold the password. */
    public function testBusyRotationIsRefusedNotQueued(): void
    {
        $this->installed();
        $this->agent->respond('db.user', new AgentBusy('site:shop already has a job running'));

        $tab = $this->tab($this->site())->callAction('rotate', ['current_password' => self::PASSWORD]);

        $tab->assertNotified(__('zimam.databases.rotate.busy'))->assertSet('rotationKey', null);
        $this->assertSame(0, AgentJob::count());
        $this->assertSame(0, DB::table('jobs')->count());
        $password = $this->submitted('db.user')[0]['password'];
        $this->assertNoTableHolds($password);
        $tab->assertDontSeeHtml($password);
    }

    public function testWriteToEnvSetsDbKeys(): void
    {
        $this->installed();
        $site = $this->site();
        $this->database($site, 'zeta');
        $this->database($site, 'alpha');
        $this->agent->env('shop', "# Shop\nAPP_NAME=Shop\nDB_CONNECTION=sqlite\nDB_HOST=localhost\nAPP_KEY=base64:abc=\nDB_HOST=duplicate\n");
        $tab = $this->tab($site);
        $password = $this->rotate($tab);
        $this->trackToTheEnd(AgentJob::sole());
        $tab->call('refreshRotation');

        // The first database by name is the default.
        $tab->assertSet('envDatabase', 's_shop_alpha')
            ->call('writeEnv', $password)
            ->assertHasNoErrors()
            ->assertSet('envWritten', true)
            ->assertNotified(__('zimam.databases.write_env.submitted', ['name' => 'shop']));

        $write = AgentJob::where('command', 'site.env.write')->sole();
        $this->assertSame(['name' => 'shop', 'content' => '[redacted]'], $write->params);
        $this->trackToTheEnd($write);
        $this->assertSame(
            "# Shop\nAPP_NAME=Shop\nDB_CONNECTION=mysql\nDB_HOST=127.0.0.1\nAPP_KEY=base64:abc=\nDB_PORT=3306\nDB_DATABASE=s_shop_alpha\nDB_USERNAME=s_shop\nDB_PASSWORD={$password}\n",
            $this->agent->envOf('shop'),
        );
        $this->assertNotInState($tab, $password);
        $this->assertNoTableHolds($password);
        $this->assertSame(['name' => 'shop', 'content' => '[redacted]'], AgentAction::where('command', 'site.env.write')->sole()->params);
    }

    public function testWriteToEnvUsesTheChosenDatabaseAndMakesAMissingEnv(): void
    {
        $this->installed();
        $site = $this->site();
        $this->database($site, 'alpha');
        $this->database($site, 'beta');
        $tab = $this->tab($site);
        $password = $this->rotate($tab);
        $this->trackToTheEnd(AgentJob::sole());

        $tab->set('envDatabase', 's_shop_beta')->call('writeEnv', $password)->assertHasNoErrors();

        $this->trackToTheEnd(AgentJob::where('command', 'site.env.write')->sole());
        $this->assertStringContainsString("DB_DATABASE=s_shop_beta\n", $this->agent->envOf('shop'));
    }

    public function testWriteToEnvRefusesADatabaseOfAnotherSiteOrNone(): void
    {
        $this->installed();
        $site = $this->site();
        $this->database($this->site('blog'), 'main');
        $tab = $this->tab($site);
        $password = $this->rotate($tab);
        $this->trackToTheEnd(AgentJob::sole());

        $tab->call('writeEnv', $password)->assertHasErrors(['envDatabase']);
        $tab->set('envDatabase', 's_blog_main')->call('writeEnv', $password)->assertHasErrors(['envDatabase']);

        $this->assertSame([], $this->submitted('site.env.write'));
    }

    /** Only a rotation whose job succeeded writes: a client calling writeEnv() on its own changes nothing. */
    public function testWriteToEnvNeedsASucceededRotation(): void
    {
        $this->installed();
        $site = $this->site();
        $this->database($site);
        $this->agent->env('shop', "APP_NAME=Shop\n");

        $this->tab($site)->call('writeEnv', str_repeat('a', 43));
        $tab = $this->tab($site);
        $password = $this->rotate($tab);
        $tab->call('writeEnv', $password);

        $this->assertSame([], $this->submitted('site.env.write'));
        $this->assertSame([], $this->submitted('site.env.read'));
    }

    public function testWriteToEnvRefusesAMalformedPassword(): void
    {
        $this->installed();
        $site = $this->site();
        $this->database($site);
        $tab = $this->tab($site);
        $this->rotate($tab);
        $this->trackToTheEnd(AgentJob::sole());

        $tab->call('writeEnv', "short\n")->call('writeEnv', ['array'])->call('writeEnv', str_repeat('a', 43)."\n");

        $this->assertSame([], $this->submitted('site.env.write'));
    }

    /** The write needs the password confirmation to be as fresh as a confirmation lasts. */
    public function testWriteToEnvExpiresWithThePasswordConfirmation(): void
    {
        $this->installed();
        $site = $this->site();
        $this->database($site);
        $tab = $this->tab($site);
        $password = $this->rotate($tab);
        $this->trackToTheEnd(AgentJob::sole());

        $this->travel((int) config('auth.password_timeout') + 1)->seconds();
        $tab->call('writeEnv', $password)
            ->assertNotified(__('zimam.databases.write_env.expired'))
            ->assertSet('rotationKey', null);

        $this->assertSame([], $this->submitted('site.env.write'));
    }

    /** A busy site's .env write is refused too, so the password never waits in the queue. */
    public function testBusyEnvWriteIsRefusedNotQueued(): void
    {
        $this->installed();
        $site = $this->site();
        $this->database($site);
        $tab = $this->tab($site);
        $password = $this->rotate($tab);
        $this->trackToTheEnd(AgentJob::sole());
        $this->agent->respond('site.env.write', new AgentBusy('site:shop already has a job running'));

        $tab->call('writeEnv', $password)->assertNotified(__('zimam.databases.rotate.busy'))->assertSet('envWritten', false);

        $this->assertSame(0, AgentJob::where('command', 'site.env.write')->count());
        $this->assertNoTableHolds($password);
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
        ];
    }

    #[DataProvider('lockedProperties')]
    public function testStateCannotBeSetFromTheBrowser(string $property, mixed $value): void
    {
        $this->expectException(CannotUpdateLockedPropertyException::class);

        $this->tab($this->site())->set($property, $value);
    }
}
