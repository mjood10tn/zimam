<?php

namespace Tests\Feature\Security;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Filament\Pages\Security;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\Certificate;
use App\Models\SecurityFinding;
use App\Models\Site;
use App\Models\User;
use Illuminate\Console\Scheduling\Event;
use Illuminate\Console\Scheduling\Schedule;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** security.check's findings, the Security screen and the daily run (§7.3, §9.7). */
final class SecurityChecksTest extends TestCase
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
        $site = Site::create(['name' => $name, 'type' => Site::PHP, 'php_version' => '8.5', 'status' => $status]);
        $site->domains()->create(['hostname' => "{$name}.example.com", 'is_primary' => true]);
        $site->securitySettings()->create();

        return $site;
    }

    private function trackAll(): void
    {
        foreach (AgentJob::all() as $job) {
            for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
                $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
                app()->call([$track, 'handle']);
            }
        }
    }

    /** @param list<array{check: string, status: string, detail: string, expires_at: ?string}> $findings */
    private function scriptFindings(string $scope, ?string $name, array $findings): void
    {
        $this->agent->scriptJob('security.check', ['running', 'succeeded'], result: array_filter(['scope' => $scope, 'name' => $name, 'findings' => $findings], fn ($v) => $v !== null));
    }

    private function finding(string $check, string $status, string $detail = 'Detail.', ?string $expiresAt = null): array
    {
        return ['check' => $check, 'status' => $status, 'detail' => $detail, 'expires_at' => $expiresAt];
    }

    /** The latest result per (scope, site, check) is kept: a second run replaces the first. */
    public function testFindingsUpserted(): void
    {
        $site = $this->site();
        $this->scriptFindings('site', 'shop', [$this->finding('dotfiles_blocked', 'fail', 'A dotfile answered 200.'), $this->finding('https_redirect', 'pass')]);
        Artisan::call('zimam:run-security-checks', ['--site' => 'shop']);
        $this->trackAll();
        $this->assertSame(2, SecurityFinding::query()->count());

        $this->travel(1)->day();
        $this->scriptFindings('site', 'shop', [$this->finding('dotfiles_blocked', 'pass', 'A dotfile answered 403.'), $this->finding('https_redirect', 'pass')]);
        Artisan::call('zimam:run-security-checks', ['--site' => 'shop']);
        $this->trackAll();

        $this->assertSame(2, SecurityFinding::query()->count());
        $dotfiles = SecurityFinding::query()->where('check', 'dotfiles_blocked')->sole();
        $this->assertSame('pass', $dotfiles->status);
        $this->assertSame('A dotfile answered 403.', $dotfiles->detail);
        $this->assertTrue($dotfiles->site->is($site));
        $this->assertTrue($dotfiles->checked_at->eq(now()));

        // Server findings have no site, and are upserted the same way.
        foreach (['fail', 'warn'] as $status) {
            $this->scriptFindings('server', null, [$this->finding('firewall_active', $status)]);
            Artisan::call('zimam:run-security-checks', ['--server' => true]);
            $this->trackAll();
        }
        $server = SecurityFinding::query()->where('scope', 'server')->sole();
        $this->assertNull($server->site_id);
        $this->assertSame('warn', $server->status);
    }

    /** certificate_valid carries the expiry, which is how the panel learns about renewals (§10). */
    public function testExpiryUpdatedFromFinding(): void
    {
        $site = $this->site();
        $site->certificate()->create(['status' => Certificate::ISSUED, 'expires_at' => now()->addDays(5)]);
        $this->scriptFindings('site', 'shop', [$this->finding('certificate_valid', 'pass', 'Valid.', '2027-02-01T09:00:00Z')]);
        Artisan::call('zimam:run-security-checks', ['--site' => 'shop']);
        $this->trackAll();

        $this->assertSame('2027-02-01 09:00:00', $site->certificate()->sole()->expires_at->utc()->format('Y-m-d H:i:s'));
    }

    /** Run checks now submits the server's checks and every active site's. */
    public function testRunNowSubmitsAll(): void
    {
        $this->site('shop');
        $this->site('blog');
        $this->site('broken', Site::FAILED);

        Livewire::test(Security::class)->callAction('runChecks')->assertNotified(__('zimam.checks.submitted'));

        $submitted = array_values(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit' && $c['command'] === 'security.check'), 'params'));
        $this->assertEqualsCanonicalizing([['scope' => 'server'], ['scope' => 'site', 'name' => 'blog'], ['scope' => 'site', 'name' => 'shop']], $submitted);
        $this->assertSame(3, AgentJob::query()->where('command', 'security.check')->count());
    }

    public function testScheduledAt0330(): void
    {
        $events = collect(app(Schedule::class)->events())
            ->filter(fn (Event $event): bool => str_contains((string) $event->command, 'zimam:run-security-checks'));

        $this->assertCount(1, $events);
        $this->assertSame('30 3 * * *', $events->first()->expression);
    }

    /** The screen shows each site's HTTPS state, expiry, protections and failing checks, and the server's checks. */
    public function testSecurityPageShowsFailing(): void
    {
        $site = $this->site();
        $site->certificate()->create(['status' => Certificate::ISSUED, 'expires_at' => now()->addDays(10)]);
        $site->securitySettings()->update(['force_https' => true, 'block_risky_functions' => true, 'password_protection' => true]);
        $site->securityFindings()->create(['scope' => 'site', 'check' => 'dotfiles_blocked', 'status' => 'fail', 'detail' => 'A dotfile answered 200.', 'checked_at' => now()]);
        $site->securityFindings()->create(['scope' => 'site', 'check' => 'https_redirect', 'status' => 'pass', 'detail' => 'HTTP answers 301.', 'checked_at' => now()]);
        SecurityFinding::query()->create(['scope' => 'server', 'check' => 'firewall_active', 'status' => 'fail', 'detail' => 'UFW isn’t active.', 'checked_at' => now()]);
        SecurityFinding::query()->create(['scope' => 'server', 'check' => 'reboot_required', 'status' => 'pass', 'detail' => 'No reboot is needed.', 'checked_at' => now()]);

        $this->get('/admin/security')->assertOk()->assertSeeInOrder([
            'shop',
            __('zimam.checks.https.forced'),
            '13 Nov 2026',
            __('zimam.checks.protections.block_risky_functions'),
            __('zimam.checks.protections.password_protection'),
            __('zimam.checks.names.dotfiles_blocked'), 'A dotfile answered 200.',
            __('zimam.checks.server'),
            __('zimam.checks.names.firewall_active'), 'UFW isn’t active.',
            __('zimam.checks.names.reboot_required'),
        ])->assertDontSee('HTTP answers 301.');
    }
}
