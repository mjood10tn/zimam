<?php

namespace Tests\Feature\Cache;

use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentUnavailable;
use App\Filament\Pages\Cache;
use App\Filament\Pages\Databases;
use App\Filament\Resources\SiteResource\Pages\CacheTab;
use App\Filament\Resources\SiteResource\Pages\DatabaseTab;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
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
}
