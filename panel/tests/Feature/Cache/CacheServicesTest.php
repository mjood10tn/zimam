<?php

namespace Tests\Feature\Cache;

use App\Agent\Exceptions\AgentConflict;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Filament\Widgets\ServiceStates;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteCredential;
use Illuminate\Support\Carbon;
use Livewire\Livewire;
use Tests\Feature\Databases\DatabasesTestCase;

/** Valkey on the dashboard and the monitoring page, and what deleting a site says about its keys. */
final class CacheServicesTest extends DatabasesTestCase
{
    protected function setUp(): void
    {
        parent::setUp();
        $this->travelTo(Carbon::parse('2026-11-01 03:41:20', 'UTC'));
        auth()->user()->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
    }

    public function testDashboardShowsValkeyOnceInstalled(): void
    {
        $this->get('/admin')->assertOk()->assertDontSee('Valkey');

        $this->agent->installValkey('9.0.4');
        $this->travel(11)->seconds();
        $this->get('/admin')->assertOk()->assertSeeInOrder(['PHP', 'Valkey', '9.0.4, running', 'Panel domain']);

        $this->agent->changeServer(function (array $status): array {
            $status['services']['valkey']['active'] = false;

            return $status;
        });
        $this->travel(11)->seconds();
        $this->get('/admin')->assertOk()->assertSee('9.0.4, not running');
    }

    public function testMonitoringShowsValkeyOnceInstalled(): void
    {
        $this->get('/admin/monitoring')->assertOk()->assertDontSee('valkey-server.service');

        $this->agent->installValkey();
        $this->travel(10)->seconds();

        $this->get('/admin/monitoring')->assertOk()->assertSee('valkey-server.service');
        Livewire::test(ServiceStates::class)->assertSee('valkey-server.service');
    }

    /** The dialog says the site's cache keys go with it, but only for a site that has a Valkey user. */
    public function testDeleteDialogMentionsCacheKeysOnlyForASiteWithAValkeyUser(): void
    {
        $site = $this->site();
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->mountAction('delete')
            ->assertMountedActionModalDontSee(__('zimam.sites.delete.cache_keys'));

        SiteCredential::create(['site_id' => $site->id, 'kind' => SiteCredential::VALKEY, 'username' => 's_shop', 'rotated_at' => now()]);
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->mountAction('delete')
            ->assertMountedActionModalSee(__('zimam.sites.delete.cache_keys'));
    }

    public function testDeleteRemovesTheValkeyUserOnTheServerAndTheCredentialRow(): void
    {
        $site = $this->site();
        $this->agent->installValkey()->cacheUser('shop');
        SiteCredential::create(['site_id' => $site->id, 'kind' => SiteCredential::VALKEY, 'username' => 's_shop', 'rotated_at' => now()]);

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->callAction('delete', ['current_password' => self::PASSWORD]);
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(0, Site::count());
        $this->assertSame(0, SiteCredential::count());
        $this->assertSame([], $this->agent->cacheUserNames());
    }

    /** With Valkey down the agent refuses: its words are shown and the site stays. */
    public function testDeleteShowsTheAgentsRefusalWhenValkeyIsDown(): void
    {
        $site = $this->site();
        $message = 'Valkey isn’t answering, so the site’s keys can’t be removed. Start it and try again.';
        $this->agent->respond('site.destroy', new AgentConflict($message));

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->callAction('delete', ['current_password' => self::PASSWORD])
            ->assertNotified($message);

        $this->assertSame(Site::ACTIVE, $site->refresh()->status);
    }
}
