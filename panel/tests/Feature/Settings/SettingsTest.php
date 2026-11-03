<?php

namespace Tests\Feature\Settings;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Filament\Pages\Settings;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\User;
use App\Settings\PanelSettings;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** Settings (§7.3): retention, the audit log's retention, the per-site cap, and the panel IP allowlist. */
final class SettingsTest extends TestCase
{
    use RefreshDatabase;

    private const GIB = 1024 ** 3;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
    }

    /** @return list<array<string, mixed>> */
    private function policies(): array
    {
        return array_values(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit' && $c['command'] === 'logs.policy'), 'params'));
    }

    public function testSettingsSubmitsPolicy(): void
    {
        Livewire::test(Settings::class)
            ->assertFormSet(['log_retention_days' => 14, 'audit_retention_days' => 365, 'site_log_cap_gib' => 5, 'ip_allowlist' => ''])
            ->fillForm(['log_retention_days' => 30, 'audit_retention_days' => 730, 'site_log_cap_gib' => 2])
            ->call('save')
            ->assertHasNoFormErrors()
            ->assertNotified(__('zimam.settings.policy_submitted'));

        $this->assertSame([['retention_days' => 30, 'audit_retention_days' => 730, 'site_cap_bytes' => 2 * self::GIB]], $this->policies());
        $this->assertSame('logs.policy', AgentJob::sole()->command);
        // Stored only once the agent has applied it.
        $this->assertSame(14, app(PanelSettings::class)->logRetentionDays());

        // Saving again without a change sends nothing.
        AgentJob::query()->update(['status' => AgentJob::SUCCEEDED]);
        Livewire::test(Settings::class)->call('save');
        $this->assertCount(1, $this->policies());
    }

    public function testOutcomeStoresPolicy(): void
    {
        Livewire::test(Settings::class)
            ->fillForm(['log_retention_days' => 1, 'audit_retention_days' => 90, 'site_log_cap_gib' => 1])
            ->call('save');
        $job = AgentJob::sole();
        for ($i = 0; $i < 5 && ! $job->refresh()->isFinished(); $i++) {
            app()->call([(new TrackAgentJob($job->fresh()))->withFakeQueueInteractions(), 'handle']);
        }

        $settings = app(PanelSettings::class);
        $this->assertSame(1, $settings->logRetentionDays());
        $this->assertSame(90, $settings->auditRetentionDays());
        $this->assertSame(self::GIB, $settings->siteLogCapBytes());
        Livewire::test(Settings::class)->assertFormSet(['log_retention_days' => 1, 'audit_retention_days' => 90, 'site_log_cap_gib' => 1]);
    }

    /** The cap is entered in whole GiB, 1 to 100, as §5.3's 1–100 GiB. */
    public function testCapEnteredInGiB(): void
    {
        foreach ([0, 101, 1.5] as $bad) {
            Livewire::test(Settings::class)->fillForm(['site_log_cap_gib' => $bad])->call('save')->assertHasFormErrors(['site_log_cap_gib']);
        }
        foreach (['log_retention_days' => [0, 366], 'audit_retention_days' => [0, 3651]] as $field => $values) {
            foreach ($values as $bad) {
                Livewire::test(Settings::class)->fillForm([$field => $bad])->call('save')->assertHasFormErrors([$field]);
            }
        }
        $this->assertSame([], $this->policies());
        Livewire::test(Settings::class)->fillForm(['site_log_cap_gib' => 100])->call('save')->assertHasNoFormErrors();
        $this->assertSame(100 * self::GIB, $this->policies()[0]['site_cap_bytes']);
    }

    /** The allowlist is stored in the panel only, and the middleware enforces it on every route. */
    public function testAllowlistSavedAndEnforced(): void
    {
        Livewire::test(Settings::class)
            ->fillForm(['ip_allowlist' => "127.0.0.0/8\n2001:db8::/32"])
            ->call('save')
            ->assertHasNoFormErrors();
        $this->assertSame(['127.0.0.0/8', '2001:db8::/32'], app(PanelSettings::class)->panelIpAllowlist());
        $this->assertSame([], $this->policies(), 'the allowlist never goes to the agent');
        $this->get('/admin/settings')->assertOk();

        Livewire::test(Settings::class)->fillForm(['ip_allowlist' => '10.0.0.1/8'])->call('save')->assertHasFormErrors(['ip_allowlist']);

        app(PanelSettings::class)->setPanelIpAllowlist(['10.0.0.0/8']);
        $this->get('/admin/settings')->assertForbidden();
    }

    /** A list that leaves out the current address needs an explicit confirmation naming the recovery command (§4). */
    public function testSelfLockoutNeedsConfirmation(): void
    {
        $page = Livewire::test(Settings::class)
            ->fillForm(['ip_allowlist' => '10.0.0.0/8'])
            ->call('save')
            ->assertHasFormErrors(['confirm_lockout'])
            ->assertSee('sudo zimam-agent panel setup-link --clear-ip-allowlist');
        $this->assertSame([], app(PanelSettings::class)->panelIpAllowlist());

        $page->fillForm(['confirm_lockout' => true])->call('save')->assertHasNoFormErrors();
        $this->assertSame(['10.0.0.0/8'], app(PanelSettings::class)->panelIpAllowlist());
    }
}
