<?php

namespace Tests\Feature\Cache;

use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentUnavailable;
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
     * service out of the status: unknown, not "not installed". Nothing throws,
     * and the panel's own record of the service stands in.
     */
    public function testPagesRenderWhenTheStatusLeavesServicesUnknown(): void
    {
        $site = $this->site();
        $this->agent->installMysql()->installValkey();
        ServerService::create(['name' => ServerService::MYSQL, 'status' => ServerService::INSTALLED, 'version' => '8.4.13']);
        ServerService::create(['name' => ServerService::VALKEY, 'status' => ServerService::INSTALLED, 'version' => '9.0.4']);
        foreach ([[], ['mysql' => null, 'valkey' => null]] as $services) {
            $data = $this->agent->call(new SystemStatus)->data;
            $data['services'] = $services;
            CacheStore::forever('zimam.system_status', ['data' => $data, 'issued_at' => now()->toIso8601String()]);

            $this->assertNull(ServerStatus::current()->service(ServerService::MYSQL));
            Livewire::test(DatabaseTab::class, ['site' => $site])->assertOk()->assertDontSee(__('zimam.databases.not_installed'));
            Livewire::test(CacheTab::class, ['site' => $site])->assertOk()->assertDontSee(__('zimam.cache.tab.not_installed'));
            Livewire::test(Databases::class)->assertOk();
            Livewire::test(Cache::class)->assertOk();
            $this->get('/admin')->assertOk();
        }
    }
}
