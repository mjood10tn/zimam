<?php

namespace Tests\Feature\Databases;

use App\Actions\SyncMysql;
use App\Agent\Exceptions\AgentUnavailable;
use App\Filament\Widgets\ServiceStates;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\ServerService;
use Illuminate\Support\Carbon;
use Livewire\Livewire;

/** MySQL on the dashboard and the monitoring page, and the sync with system.status. */
final class ServicesTest extends DatabasesTestCase
{
    protected function setUp(): void
    {
        parent::setUp();
        $this->travelTo(Carbon::parse('2026-11-01 03:41:20', 'UTC'));
        auth()->user()->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
    }

    public function testServicesShownOnDashboard(): void
    {
        $this->agent->installMysql('8.4.13');

        $this->get('/admin')->assertOk()->assertSeeInOrder(['PHP', 'MySQL', '8.4.13, running', 'Panel domain']);

        $this->agent->changeServer(function (array $status): array {
            $status['services']['mysql']['active'] = false;

            return $status;
        });
        $this->travel(11)->seconds();

        $this->get('/admin')->assertOk()->assertSee('8.4.13, not running');
    }

    /** A service that isn't installed isn't mentioned. */
    public function testDashboardSaysNothingOfMysqlUntilItIsInstalled(): void
    {
        $this->get('/admin')->assertOk()->assertSee('Panel domain')->assertDontSeeHtml('<dt>MySQL</dt>');
    }

    public function testMonitoringShowsMysqlOnceInstalled(): void
    {
        $this->get('/admin/monitoring')->assertOk()->assertSee('nginx.service')->assertDontSee('mysql.service');

        $this->agent->installMysql();
        $this->travel(10)->seconds();

        $this->get('/admin/monitoring')->assertOk()->assertSee('mysql.service');
        Livewire::test(ServiceStates::class)->assertSee('mysql.service');
    }

    /** Monitoring still renders with MySQL stopped: a warning names its unit. */
    public function testMonitoringShowsAStoppedMysql(): void
    {
        $this->agent->installMysql();
        $this->agent->changeMetrics(function (array $metrics): array {
            foreach ($metrics['services'] as &$service) {
                if ($service['unit'] === 'mysql.service') {
                    $service['active_state'] = 'failed';
                }
            }

            return $metrics;
        });

        $this->get('/admin/monitoring')->assertOk()->assertSee('mysql.service');
    }

    public function testSyncRecordsTheInstalledVersionFromSystemStatus(): void
    {
        $this->agent->installMysql('8.4.13');

        $this->assertTrue(app(SyncMysql::class)->run());

        $service = ServerService::named(ServerService::MYSQL);
        $this->assertSame(ServerService::INSTALLED, $service->status);
        $this->assertSame('8.4.13', $service->version);
    }

    public function testSyncDropsAServiceRowTheAgentDoesNotHave(): void
    {
        ServerService::create(['name' => ServerService::MYSQL, 'status' => ServerService::INSTALLED, 'version' => '8.4.13']);

        app(SyncMysql::class)->run();

        $this->assertNull(ServerService::named(ServerService::MYSQL));
    }

    public function testSyncChangesNothingWhenTheAgentIsDown(): void
    {
        $site = $this->site();
        $row = $this->database($site);
        $this->agent->respond('system.status', new AgentUnavailable('down'));

        $this->assertFalse(app(SyncMysql::class)->run());

        $this->assertTrue(Database::whereKey($row->id)->exists());
    }

    /** A row saved just before its job is recorded is still being created or deleted: a sync in that window leaves it. */
    public function testSyncLeavesRowsWhoseJobIsNotRecordedYet(): void
    {
        $site = $this->site();
        $creating = Database::create(['site_id' => $site->id, 'name' => 's_shop_new', 'status' => Database::CREATING]);
        $dropping = $this->database($site, 'old');
        $dropping->update(['status' => Database::DROPPING]);
        // The agent lists the one being dropped and not the one being created.
        $this->agent->database('shop', 's_shop_old');

        app(SyncMysql::class)->run();

        $this->assertSame(Database::CREATING, $creating->refresh()->status);
        $this->assertSame(Database::DROPPING, $dropping->refresh()->status);
    }

    /** A row that has waited longer than the window with no job is stuck, and is reconciled. */
    public function testSyncReconcilesAJobLessRowOnceItIsStuck(): void
    {
        $site = $this->site();
        $creating = Database::create(['site_id' => $site->id, 'name' => 's_shop_new', 'status' => Database::CREATING]);
        $this->agent->installMysql();
        $this->travel(6)->minutes();

        app(SyncMysql::class)->run();

        $this->assertNull(Database::find($creating->id));
    }

    public function testSyncKeepsAnUnfinishedJobsRowAndClearsTheErrorOfOneThatIsBack(): void
    {
        $site = $this->site();
        $running = $this->database($site, 'running');
        $running->update(['status' => Database::DROPPING]);
        $running->jobs()->create(['command' => 'db.drop', 'status' => AgentJob::RUNNING, 'params' => []]);
        $failed = $this->database($site, 'failed');
        $failed->update(['status' => Database::FAILED, 'last_error' => 'MySQL refused the CREATE']);

        app(SyncMysql::class)->run();

        $this->assertSame(Database::DROPPING, $running->refresh()->status);
        $failed->refresh();
        $this->assertSame(Database::ACTIVE, $failed->status);
        $this->assertNull($failed->last_error);
    }

    /** A database of a site the panel doesn't know has no row to hang on. */
    public function testSyncIgnoresADatabaseOfAnUnknownSite(): void
    {
        $this->agent->installMysql()->database('ghost', 's_ghost_main');

        app(SyncMysql::class)->run();

        $this->assertSame(0, Database::count());
    }
}
