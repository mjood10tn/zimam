<?php

namespace Tests\Feature\Sites;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\Certificate;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** A site's certificate (§10): issuing it, its outcome, and the warnings as it nears its end. */
final class CertificateTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->travelTo(Carbon::parse('2026-11-03 09:00:00', 'UTC'));
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
    }

    private function site(string $name = 'shop', string $status = Site::ACTIVE): Site
    {
        $site = Site::create(['name' => $name, 'type' => 'static', 'status' => $status]);
        $site->domains()->create(['hostname' => "{$name}.example.com", 'is_primary' => true]);
        $site->securitySettings()->create();

        return $site;
    }

    private function trackToTheEnd(AgentJob $job): void
    {
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
            app()->call([$track, 'handle']);
        }
    }

    public function testIssueSubmits(): void
    {
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->assertActionVisible('issueCertificate')
            ->callAction('issueCertificate')
            ->assertNotified(__('zimam.sites.certificate.submitted', ['name' => 'shop']));

        $submits = array_values(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit'));
        $this->assertSame([['command' => 'cert.issue', 'params' => ['name' => 'shop']]], array_map(fn (array $c): array => ['command' => $c['command'], 'params' => $c['params']], $submits));
        $certificate = $site->refresh()->certificate;
        $this->assertSame(Certificate::ISSUING, $certificate->status);
        $this->assertTrue(AgentJob::sole()->related->is($certificate));
    }

    public function testOutcomeStoresExpiry(): void
    {
        $site = $this->site();
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->callAction('issueCertificate');

        $this->trackToTheEnd(AgentJob::sole());

        $certificate = $site->refresh()->certificate;
        $this->assertSame(Certificate::ISSUED, $certificate->status);
        $this->assertSame('2027-02-01 09:00:00', $certificate->expires_at->utc()->format('Y-m-d H:i:s'));
        $this->assertNull($certificate->last_error);
        $this->assertTrue($site->securitySettings->force_https);
        $this->assertSame('https://shop.example.com', $site->url());
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->assertSee(__('zimam.sites.certificate.issued', ['date' => '1 Feb 2027']))
            ->assertActionHidden('issueCertificate');
    }

    public function testFailureShowsRetry(): void
    {
        $this->agent->scriptJob('cert.issue', ['running', 'failed'], errorCode: 'internal', message: "certbot couldn't get a certificate for shop.example.com:\nTimeout during connect");
        $site = $this->site();
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->callAction('issueCertificate');

        $this->trackToTheEnd(AgentJob::sole());

        $certificate = $site->refresh()->certificate;
        $this->assertSame(Certificate::FAILED, $certificate->status);
        $this->assertStringContainsString('Timeout during connect', $certificate->last_error);
        $this->assertFalse($site->securitySettings->force_https);
        $this->assertSame('http://shop.example.com', $site->url());
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->assertSee('Timeout during connect')
            ->assertActionVisible('issueCertificate')
            ->assertActionHasLabel('issueCertificate', __('zimam.sites.certificate.retry'));
    }

    public function testIssueOnlyForActiveSites(): void
    {
        Livewire::test(ViewSite::class, ['record' => $this->site('shop', Site::FAILED)->getRouteKey()])->assertActionHidden('issueCertificate');
    }

    /** Certificates within 14 days of their end are a warning; an expired one is critical, and comes first. */
    public function testExpiryWarning(): void
    {
        $this->site('soon')->certificate()->create(['status' => Certificate::ISSUED, 'expires_at' => now()->addDays(10)]);
        $this->site('gone')->certificate()->create(['status' => Certificate::ISSUED, 'expires_at' => now()->subDay()]);
        $this->site('fine')->certificate()->create(['status' => Certificate::ISSUED, 'expires_at' => now()->addDays(60)]);

        $this->get('/admin')->assertOk()->assertSeeInOrder([
            __('zimam.dashboard.warnings.certificate_expired.title', ['site' => 'gone']),
            trans_choice('zimam.dashboard.warnings.certificate_expiring.title', 10, ['site' => 'soon', 'days' => 10]),
        ])->assertDontSee('fine.example.com')->assertDontSee('The certificate for fine');
    }
}
