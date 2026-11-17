<?php

namespace Tests\Feature\Databases;

use App\Agent\Exceptions\AgentConflict;
use App\Filament\Pages\Databases;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\ServerService;
use Livewire\Livewire;
use PHPUnit\Framework\Attributes\DataProvider;

/** The MySQL page: install, every database with its site and size, create and drop. */
final class DatabasesPageTest extends DatabasesTestCase
{
    public function testInstallMysqlSubmits(): void
    {
        Livewire::test(Databases::class)
            ->assertSee(__('zimam.databases.not_installed'))
            ->assertActionVisible('install')
            ->assertActionHidden('create')
            ->callAction('install')
            ->assertNotified(__('zimam.databases.install.submitted'));

        $this->assertSame([[]], $this->submitted('mysql.install'));
        $service = ServerService::named(ServerService::MYSQL);
        $this->assertSame(ServerService::INSTALLING, $service->status);
        $job = AgentJob::sole();
        $this->assertTrue($job->related->is($service));
        $this->assertSame(900, $job->runtime_max_seconds);

        // While it installs there is nothing to offer twice.
        Livewire::test(Databases::class)->assertActionHidden('install');

        $this->trackToTheEnd($job);
        $service->refresh();
        $this->assertSame(AgentJob::SUCCEEDED, $job->status);
        $this->assertSame(ServerService::INSTALLED, $service->status);
        $this->assertSame('8.4.13', $service->version);
        Livewire::test(Databases::class)
            ->assertActionHidden('install')
            ->assertActionVisible('create')
            ->assertDontSee(__('zimam.databases.not_installed'));
    }

    public function testFailedInstallKeepsTheReasonAndCanBeTriedAgain(): void
    {
        $this->agent->scriptJob('mysql.install', ['running', 'failed'], errorCode: 'internal', message: 'apt-get failed: no space left on device');
        Livewire::test(Databases::class)->callAction('install');

        $this->trackToTheEnd(AgentJob::sole());

        $service = ServerService::named(ServerService::MYSQL);
        $this->assertSame(ServerService::FAILED, $service->status);
        $this->assertSame('apt-get failed: no space left on device', $service->last_error);
        Livewire::test(Databases::class)->assertActionVisible('install');
    }

    public function testInstallRefusalPutsTheRowBack(): void
    {
        $this->agent->respond('mysql.install', new AgentConflict('Another install is running.'));

        Livewire::test(Databases::class)->callAction('install')->assertNotified('Another install is running.');

        $this->assertNull(ServerService::named(ServerService::MYSQL));
    }

    public function testPageListsDatabasesWithSiteAndSize(): void
    {
        $shop = $this->site();
        $blog = $this->site('my-blog');
        $this->database($shop, 'main', 3 * 1024 * 1024);
        $this->database($blog, 'wp', 0);

        Livewire::test(Databases::class)
            ->assertSee(['s_shop_main', 'shop', '3 MiB', 's_my_blog_wp', 'my-blog'])
            ->assertSee(__('zimam.databases.sizes_note'));
    }

    /** The agent's list is the truth: what it lacks goes, what the panel lacks comes. */
    public function testPageSyncsWithTheAgentList(): void
    {
        $shop = $this->site();
        $gone = $this->database($shop, 'gone');
        $this->agent->installMysql()->database('shop', 's_shop_new', 2048);
        // The agent no longer reports the first one.
        $this->agent->respond('db.list', ['databases' => [['name' => 's_shop_new', 'site' => 'shop', 'size_bytes' => 2048]]]);

        Livewire::test(Databases::class);

        $this->assertNull(Database::where('name', $gone->name)->first());
        $new = Database::where('name', 's_shop_new')->sole();
        $this->assertSame($shop->id, $new->site_id);
        $this->assertSame(2048, $new->size_bytes);
        $this->assertSame(Database::ACTIVE, $new->status);
    }

    public function testSyncKeepsADatabaseWhoseCreateIsStillRunning(): void
    {
        $shop = $this->site();
        $this->agent->installMysql();
        Livewire::test(Databases::class)->callAction('create', ['site' => 'shop', 'suffix' => 'main']);
        $this->assertSame(Database::CREATING, Database::sole()->status);

        // The agent doesn't list it yet.
        $this->agent->respond('db.list', ['databases' => []]);
        Livewire::test(Databases::class);

        $this->assertSame(Database::CREATING, Database::sole()->status);
    }

    public function testCreateSubmitsAndTheOutcomeActivates(): void
    {
        $this->agent->installMysql();
        $site = $this->site('my-shop');

        Livewire::test(Databases::class)
            ->callAction('create', ['site' => 'my-shop', 'suffix' => 'main'])
            ->assertHasNoActionErrors()
            ->assertNotified(__('zimam.databases.create.submitted', ['name' => 's_my_shop_main']));

        $this->assertSame([['site' => 'my-shop', 'database' => 's_my_shop_main']], $this->submitted('db.create'));
        $row = Database::sole();
        $this->assertSame(Database::CREATING, $row->status);
        $this->assertSame($site->id, $row->site_id);
        $this->assertTrue(AgentJob::sole()->related->is($row));

        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(Database::ACTIVE, $row->refresh()->status);
    }

    /** @return array<string, array{string}> */
    public static function badSuffixes(): array
    {
        return [
            'empty' => [''],
            'upper case' => ['Main'],
            'hyphen' => ['my-db'],
            'space' => ['my db'],
            'backtick' => ['a`b'],
            'leading underscore' => ['_main'],
            // $ would let this through; \z does not.
            'trailing newline' => ["main\n"],
            'too long' => [str_repeat('a', 60)],
        ];
    }

    #[DataProvider('badSuffixes')]
    public function testCreateDatabaseValidatesName(string $suffix): void
    {
        $this->agent->installMysql();
        $this->site();

        Livewire::test(Databases::class)
            ->callAction('create', ['site' => 'shop', 'suffix' => $suffix])
            ->assertHasActionErrors(['suffix']);

        $this->assertSame([], $this->submitted('db.create'));
        $this->assertSame(0, Database::count());
    }

    /** The suffix rule on its own, not the whole-name rule behind it: $ would let a trailing newline through. */
    public function testSuffixPatternAnchorsWithAnEndOfTextAnchor(): void
    {
        $this->agent->installMysql();
        $this->site();

        Livewire::test(Databases::class)
            ->callAction('create', ['site' => 'shop', 'suffix' => "main\n"])
            ->assertHasActionErrors(['suffix' => ['regex']]);

        $this->assertSame(1, preg_match('/^[a-z0-9][a-z0-9_]*$/', "main\n"), 'the pattern with $ would pass it');
        $this->assertSame(0, preg_match(Database::SUFFIX_PATTERN, "main\n"));
    }

    public function testCreateRefusesAnExistingNameAndAnUnknownSite(): void
    {
        $shop = $this->site();
        $this->database($shop, 'main');

        Livewire::test(Databases::class)
            ->callAction('create', ['site' => 'shop', 'suffix' => 'main'])
            ->assertHasActionErrors(['suffix']);
        Livewire::test(Databases::class)
            ->callAction('create', ['site' => 'nosuchsite', 'suffix' => 'main'])
            ->assertHasActionErrors(['site']);

        $this->assertSame([], $this->submitted('db.create'));
    }

    /** The shape rules the agent has, as the panel states them. */
    public function testNameRulesMirrorTheAgent(): void
    {
        $this->assertNull(Database::nameError('s_shop_main'));
        $this->assertSame('name', Database::nameError("s_shop_main\n"));
        $this->assertSame('name', Database::nameError('S_shop'));
        $this->assertSame('name', Database::nameError('1shop'));
        $this->assertSame('name', Database::nameError(str_repeat('a', 65)));
        foreach (['mysql', 'sys', 'information_schema', 'performance_schema', 'zimam'] as $reserved) {
            $this->assertSame('reserved', Database::nameError($reserved));
        }
        // 64 characters with 22 underscores count 86 in a grant.
        $this->assertSame('too_long', Database::nameError(str_repeat('a_', 22).str_repeat('b', 20)));
        $this->assertNull(Database::nameError(str_repeat('a', 62).'_'));
        $this->assertSame('too_long', Database::nameError(str_repeat('a', 63).'_'));
    }

    /** Which site a name belongs to is the agent's call: its refusal is shown as it worded it. */
    public function testAgentConflictIsShownAndTheRowRemoved(): void
    {
        $this->agent->installMysql();
        $this->site('shop');
        $this->agent->respond('db.create', new AgentConflict('Database s_shop_x_main belongs to site shop-x.'));

        Livewire::test(Databases::class)
            ->callAction('create', ['site' => 'shop', 'suffix' => 'x_main'])
            ->assertNotified('Database s_shop_x_main belongs to site shop-x.');

        $this->assertSame(0, Database::count());
    }

    public function testFailedCreateStaysWithItsReasonUntilDismissed(): void
    {
        $this->agent->installMysql();
        $this->site();
        $this->agent->scriptJob('db.create', ['running', 'failed'], errorCode: 'internal', message: 'MySQL refused the CREATE');
        Livewire::test(Databases::class)->callAction('create', ['site' => 'shop', 'suffix' => 'main']);
        $this->trackToTheEnd(AgentJob::sole());

        $row = Database::sole();
        $this->assertSame(Database::FAILED, $row->status);
        $this->assertSame('MySQL refused the CREATE', $row->last_error);
        // The sync keeps a failed row: it shows why.
        Livewire::test(Databases::class)->assertSee('MySQL refused the CREATE');
        $this->assertSame(1, Database::count());

        Livewire::test(Databases::class)->callTableAction('dismiss', $row);
        $this->assertSame(0, Database::count());
    }

    public function testDropNeedsPasswordConfirmation(): void
    {
        $row = $this->database($this->site());

        Livewire::test(Databases::class)
            ->callTableAction('drop', $row, ['current_password' => 'not the password'])
            ->assertHasTableActionErrors(['current_password']);

        $this->assertSame([], $this->submitted('db.drop'));
        $this->assertSame(Database::ACTIVE, $row->refresh()->status);
    }

    public function testDropSubmitsAndTheOutcomeRemovesTheRow(): void
    {
        $row = $this->database($this->site());

        Livewire::test(Databases::class)
            ->callTableAction('drop', $row, ['current_password' => self::PASSWORD])
            ->assertHasNoTableActionErrors()
            ->assertNotified(__('zimam.databases.drop.submitted', ['name' => 's_shop_main']));

        $this->assertSame([['site' => 'shop', 'database' => 's_shop_main']], $this->submitted('db.drop'));
        $this->assertSame(Database::DROPPING, $row->refresh()->status);

        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(0, Database::count());
        $this->assertSame([], $this->agent->databaseNames());
    }

    public function testFailedDropLeavesTheDatabaseWithTheReason(): void
    {
        $row = $this->database($this->site());
        $this->agent->scriptJob('db.drop', ['running', 'failed'], errorCode: 'internal', message: 'DROP DATABASE failed');
        Livewire::test(Databases::class)->callTableAction('drop', $row, ['current_password' => self::PASSWORD]);

        $this->trackToTheEnd(AgentJob::sole());

        $row->refresh();
        $this->assertSame(Database::ACTIVE, $row->status);
        $this->assertSame('DROP DATABASE failed', $row->last_error);
    }

    public function testActionsWaitWhileTheSiteHasAJob(): void
    {
        $site = $this->site();
        $row = $this->database($site);
        $site->jobs()->create(['command' => 'site.php', 'status' => AgentJob::RUNNING, 'params' => []]);

        Livewire::test(Databases::class)->assertTableActionDisabled('drop', $row);
    }
}
