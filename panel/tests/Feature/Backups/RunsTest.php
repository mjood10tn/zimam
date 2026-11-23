<?php

namespace Tests\Feature\Backups;

use App\Filament\Pages\Backups\RunsTable;
use App\Filament\Resources\SiteResource\Pages\BackupsTab;
use App\Models\AgentJob;
use App\Models\BackupDestination;
use App\Models\BackupPlan;
use App\Models\BackupRun;
use App\Models\Database;
use Illuminate\Support\Facades\DB;
use Livewire\Livewire;

/** Runs synced from the agent's records (R05-D9), their restores (R05-D10), the site's tab and the panel snapshot. */
final class RunsTest extends BackupsTestCase
{
    private const PARTS = [
        ['part' => 'files-shop', 'bytes' => 2048, 'sha256' => 'aa'],
        ['part' => 'db-s_shop_main', 'bytes' => 1024, 'sha256' => 'bb'],
        ['part' => 'panel', 'bytes' => 512, 'sha256' => 'cc'],
    ];

    private function succeededRun(BackupPlan $plan, string $backup = '20261123T020000Z'): BackupRun
    {
        $this->agent->backupRunRecord(['plan' => $plan->key, 'backup' => $backup, 'parts' => self::PARTS]);

        return $plan->runs()->create([
            'backup_id' => $backup, 'trigger' => BackupRun::TIMER, 'status' => BackupRun::SUCCEEDED, 'size_bytes' => 3584,
            'parts' => self::PARTS, 'started_at' => now(), 'finished_at' => now(),
        ]);
    }

    public function testSyncUpsertsRuns(): void
    {
        $plan = $this->plan($this->destination(), sites: [$this->site()]);
        $this->agent->backupRunRecord(['plan' => 'nightly', 'backup' => '20261122T020000Z', 'parts' => self::PARTS])
            ->backupRunRecord(['plan' => 'nightly', 'backup' => '20261123T020000Z', 'status' => 'running'])
            ->backupRunRecord(['plan' => 'gone', 'backup' => '20261123T020000Z']);

        $this->artisan('zimam:sync-backup-runs')->assertSuccessful();

        $runs = $plan->runs()->orderBy('backup_id')->get();
        $this->assertSame(['20261122T020000Z', '20261123T020000Z'], $runs->pluck('backup_id')->all());
        $this->assertSame([BackupRun::SUCCEEDED, BackupRun::RUNNING], $runs->pluck('status')->all());
        $this->assertSame(3584, $runs[0]->size_bytes);
        $this->assertSame(2, BackupRun::count());

        // The running one finishes: the next poll asks from before it, so it is read again.
        $this->agent->backupRunRecord(['plan' => 'nightly', 'backup' => '20261123T020000Z', 'status' => 'failed', 'error' => 'mysqldump exited 2']);
        $this->artisan('zimam:sync-backup-runs')->assertSuccessful();

        $this->assertSame('20261123T015959Z', $this->agent->calls()[array_key_last($this->agent->calls())]['params']['since']);
        $run = $plan->runs()->where('backup_id', '20261123T020000Z')->sole();
        $this->assertSame(BackupRun::FAILED, $run->status);
        $this->assertSame('mysqldump exited 2', $run->error);
        $this->assertNotNull($run->finished_at);
        $this->assertSame(2, BackupRun::count());

        // With nothing running, it asks only for what came after the newest.
        $this->artisan('zimam:sync-backup-runs')->assertSuccessful();
        $this->assertSame('20261123T020000Z', $this->agent->calls()[array_key_last($this->agent->calls())]['params']['since']);
    }

    public function testSyncWithoutPlansAsksNothing(): void
    {
        $this->artisan('zimam:sync-backup-runs')->assertSuccessful();

        $this->assertSame([], $this->agent->calls());
    }

    public function testRunsTableShowsRunsAndTheirSize(): void
    {
        $plan = $this->plan($this->destination(), sites: [$this->site()]);
        $this->succeededRun($plan);
        $plan->runs()->create(['backup_id' => '20261123T030000Z', 'trigger' => 'panel', 'status' => 'failed', 'parts' => [], 'error' => 'rclone: quota exceeded', 'started_at' => now(), 'finished_at' => now()]);

        Livewire::test(RunsTable::class)->assertSee(['nightly', '3.5 KiB', 'rclone: quota exceeded']);
    }

    public function testRestoreDatabaseNeedsConfirmationToOverwrite(): void
    {
        $shop = $this->site();
        $db = $this->database($shop);
        $run = $this->succeededRun($this->plan($this->destination(), sites: [$shop], databases: [$db]));

        Livewire::test(RunsTable::class)
            ->callTableAction('overwrite', $run, ['part' => 'db-s_shop_main', 'database' => 's_shop_main'])
            ->assertHasTableActionErrors(['current_password']);
        Livewire::test(RunsTable::class)
            ->callTableAction('overwrite', $run, ['part' => 'db-s_shop_main', 'database' => 's_shop_main', 'current_password' => 'not the password'])
            ->assertHasTableActionErrors(['current_password']);
        $this->assertSame([], $this->submitted('backup.restore'));

        Livewire::test(RunsTable::class)
            ->callTableAction('overwrite', $run, ['part' => 'db-s_shop_main', 'database' => 's_shop_main', 'current_password' => self::PASSWORD])
            ->assertHasNoTableActionErrors()
            ->assertNotified(__('zimam.backups.restore.submitted'));

        $this->assertSame([[
            'plan' => 'nightly', 'backup' => '20261123T020000Z', 'part' => 'db-s_shop_main', 'site' => 'shop', 'database' => 's_shop_main', 'overwrite' => true,
        ]], $this->submitted('backup.restore'));
        $job = $this->trackToTheEnd(AgentJob::sole());
        $this->assertSame('site:shop', $job->subject);
        $this->assertTrue($job->related->is($shop));
    }

    public function testRestoreDatabaseIntoANewOne(): void
    {
        $shop = $this->site();
        $run = $this->succeededRun($this->plan($this->destination(), sites: [$shop], databases: [$this->database($shop)]));

        Livewire::test(RunsTable::class)
            ->callTableAction('restore', $run, ['part' => 'db-s_shop_main', 'suffix' => 'restored'])
            ->assertHasNoTableActionErrors();

        $this->assertSame([[
            'plan' => 'nightly', 'backup' => '20261123T020000Z', 'part' => 'db-s_shop_main', 'site' => 'shop', 'database' => 's_shop_restored',
        ]], $this->submitted('backup.restore'));
        $this->trackToTheEnd(AgentJob::sole());
        // The outcome syncs MySQL, so the new database shows up.
        $this->assertSame(Database::ACTIVE, Database::where('name', 's_shop_restored')->sole()->status);
    }

    public function testRestoreFilesGoesToTheRestoreFolderAndTheTabShowsSkippedLinks(): void
    {
        $shop = $this->site();
        $run = $this->succeededRun($this->plan($this->destination(), sites: [$shop]));
        $this->agent->scriptJob('backup.restore', ['running', 'succeeded'], result: [
            'site' => 'shop', 'part' => 'files-shop', 'folder' => '/home/s_shop/restores/20261123T020000Z', 'skipped_links' => ['site/shared/public/storage'],
        ]);

        Livewire::test(RunsTable::class)
            ->callTableAction('restore', $run, ['part' => 'files-shop'])
            ->assertHasNoTableActionErrors();

        $this->assertSame([['plan' => 'nightly', 'backup' => '20261123T020000Z', 'part' => 'files-shop', 'site' => 'shop']], $this->submitted('backup.restore'));
        $this->trackToTheEnd(AgentJob::sole());
        Livewire::test(BackupsTab::class, ['site' => $shop])
            ->assertSee(['nightly', '/home/s_shop/restores/20261123T020000Z', 'site/shared/public/storage']);
    }

    public function testThePanelPartIsNotOffered(): void
    {
        $shop = $this->site();
        $run = $this->succeededRun($this->plan($this->destination(), sites: [$shop]));

        Livewire::test(RunsTable::class)
            ->callTableAction('restore', $run, ['part' => 'panel'])
            ->assertHasTableActionErrors(['part']);
        $this->assertSame([], $this->submitted('backup.restore'));
    }

    public function testSiteTabListsItsPlansAndRuns(): void
    {
        $shop = $this->site();
        $other = $this->site('blog');
        $plan = $this->plan($this->destination(), sites: [$shop]);
        $this->plan(BackupDestination::sole(), 'blogs', sites: [$other]);
        $this->succeededRun($plan);

        Livewire::test(BackupsTab::class, ['site' => $shop])
            ->assertSee('nightly')
            ->assertDontSee('blogs');
    }

    /**
     * VACUUM INTO can't run inside the test's transaction, so this runs it
     * against a file database of its own, as the panel's is on a server.
     */
    public function testSnapshotDbProducesConsistentCopy(): void
    {
        $dir = sys_get_temp_dir().'/zimam-snapshot-'.bin2hex(random_bytes(4));
        mkdir($dir);
        $source = "{$dir}/panel.sqlite";
        $path = "{$dir}/copy.sqlite";
        touch($source);
        config(['database.connections.snapshot' => ['driver' => 'sqlite', 'database' => $source, 'prefix' => '', 'foreign_key_constraints' => true]]);
        $default = DB::getDefaultConnection();
        DB::setDefaultConnection('snapshot');
        try {
            DB::statement('PRAGMA journal_mode = WAL');
            DB::statement('CREATE TABLE sites (id INTEGER PRIMARY KEY, name TEXT)');
            DB::table('sites')->insert([['name' => 'shop'], ['name' => 'blog']]);

            $this->artisan('zimam:snapshot-db', ['path' => $path])->assertSuccessful();

            $copy = new \PDO('sqlite:'.$path);
            $this->assertSame('ok', $copy->query('PRAGMA integrity_check')->fetchColumn());
            $this->assertSame(['shop', 'blog'], $copy->query('SELECT name FROM sites ORDER BY id')->fetchAll(\PDO::FETCH_COLUMN));
            $copy = null;

            // It never writes over a file, nor to a relative path.
            $this->artisan('zimam:snapshot-db', ['path' => $path])->assertFailed();
            $this->artisan('zimam:snapshot-db', ['path' => 'relative.sqlite'])->assertFailed();
        } finally {
            DB::setDefaultConnection($default);
            DB::purge('snapshot');
            array_map('unlink', glob("{$dir}/*"));
            rmdir($dir);
        }
    }
}
