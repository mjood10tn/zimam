<?php

namespace Tests\Feature\Cache;

use App\Actions\SyncMysql;
use App\Actions\SyncValkey;
use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Agent\ServerStatus;
use App\Filament\Pages\Cache;
use App\Filament\Pages\Databases;
use App\Filament\Resources\SiteResource\Pages\CacheTab;
use App\Filament\Resources\SiteResource\Pages\DatabaseTab;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Models\ServerService;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Cache as CacheStore;
use Livewire\Livewire;
use Tests\Feature\Databases\DatabasesTestCase;

/** A status cached from an agent that predates MySQL and Valkey has no services key, and the agent may be down: nothing may throw. */
final class OldStatusTest extends DatabasesTestCase
{
    protected function setUp(): void
    {
        parent::setUp();
        $this->travelTo(Carbon::parse('2026-11-01 03:41:20', 'UTC'));
        auth()->user()->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
    }

    private function cacheOldStatusAndStopTheAgent(): void
    {
        $data = $this->agent->call(new SystemStatus)->data;
        unset($data['services']);
        CacheStore::forever('zimam.system_status', ['data' => $data, 'issued_at' => now()->toIso8601String()]);
        $this->agent->respond('agent.version', new AgentUnavailable('down'));
        $this->agent->respond('system.status', new AgentUnavailable('down'));
        CacheStore::forget('zimam.agent.reachable');
        $this->travel(60)->seconds();
    }

    public function testPagesRenderWithAStatusWithoutServicesAndNoAgent(): void
    {
        $site = $this->site();
        $this->cacheOldStatusAndStopTheAgent();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->assertOk();
        Livewire::test(CacheTab::class, ['site' => $site])->assertOk()->assertSee(__('zimam.cache.tab.not_installed'));
        Livewire::test(DatabaseTab::class, ['site' => $site])->assertOk();
        Livewire::test(Cache::class)->assertOk();
        Livewire::test(Databases::class)->assertOk();
        $this->get('/admin')->assertOk();
        $this->get('/admin/monitoring')->assertOk();
    }

    /**
     * An agent that can't read services.json, or can't ask systemd, leaves a
     * service out of the status: unknown, not "not installed". The agent
     * itself answers that way here, and the panel's record of the service
     * stands: its row survives every sync, nothing offers an install, and
     * nothing throws.
     */
    public function testUnknownServicesKeepTheirRowsAndOfferNoInstall(): void
    {
        $site = $this->site();
        $this->agent->installMysql()->installValkey();
        foreach ([ServerService::MYSQL => '8.4.13', ServerService::VALKEY => '9.0.4'] as $name => $version) {
            ServerService::create(['name' => $name, 'status' => ServerService::INSTALLED, 'version' => $version]);
        }
        foreach ([[], ['mysql' => null, 'valkey' => null]] as $services) {
            $this->agent->respond('system.status', function () use ($services): array {
                $data = (new FakeAgentClient)->call(new SystemStatus)->data;
                $data['services'] = $services;

                return $data;
            });
            CacheStore::forget('zimam.system_status');

            $this->assertNull(ServerStatus::fresh()->service(ServerService::MYSQL));
            $this->assertTrue(ServerStatus::current()->serviceUnknown(ServerService::MYSQL));
            $this->assertTrue(app(SyncMysql::class)->run() === false);
            $this->assertFalse(app(SyncValkey::class)->run());
            $this->assertFalse(app(SyncMysql::class)->runForSite($site));
            $this->assertSame(2, ServerService::where('status', ServerService::INSTALLED)->count(), 'a row was dropped');

            Livewire::test(DatabaseTab::class, ['site' => $site])->assertOk()->assertDontSee(__('zimam.databases.not_installed'));
            Livewire::test(CacheTab::class, ['site' => $site])->assertOk()->assertDontSee(__('zimam.cache.tab.not_installed'));
            Livewire::test(Databases::class)->assertOk()->assertActionHidden('install');
            Livewire::test(Cache::class)->assertOk()->assertActionHidden('install');
            $this->get('/admin')->assertOk()->assertSee(__('zimam.server.mysql_unknown'));
            $this->assertSame(2, ServerService::where('status', ServerService::INSTALLED)->count());
        }

        // With no row to fall back on, the pages say it is unknown and still offer no install.
        ServerService::query()->delete();
        Livewire::test(Databases::class)->assertSee(__('zimam.databases.unknown'))->assertActionHidden('install');
        Livewire::test(Cache::class)->assertSee(__('zimam.cache.unknown'))->assertActionHidden('install');

        // An agent that predates the services (no key at all) still means not installed.
        $this->agent->respond('system.status', function (): array {
            $data = (new FakeAgentClient)->call(new SystemStatus)->data;
            unset($data['services']);

            return $data;
        });
        CacheStore::forget('zimam.system_status');
        Livewire::test(Databases::class)->assertActionVisible('install');
    }
}
