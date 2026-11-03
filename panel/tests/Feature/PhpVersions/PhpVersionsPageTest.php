<?php

namespace Tests\Feature\PhpVersions;

use App\Agent\AgentClient;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Filament\Pages\PhpVersions;
use App\Models\AgentJob;
use App\Models\PhpVersion;
use App\Models\User;
use Filament\Actions\Testing\TestAction;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** The PHP versions screen (§7.3, §8). */
final class PhpVersionsPageTest extends TestCase
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
        $this->actingAs(User::factory()->create(['password' => 'the right password']));
    }

    /** 8.3 runs a site, 8.4 is the default and 8.5 the panel's; 8.2 is installed and unused. */
    private function server(): void
    {
        $this->agent->changeServer(function (array $status): array {
            $status['php']['installed'] = [
                ['version' => '8.2', 'fpm_active' => false, 'in_use' => false],
                ['version' => '8.3', 'fpm_active' => true, 'in_use' => true],
                ['version' => '8.4', 'fpm_active' => true, 'in_use' => true],
                ['version' => '8.5', 'fpm_active' => true, 'in_use' => true],
            ];
            $status['php']['default'] = '8.4';
            $status['php']['panel'] = '8.5';

            return $status;
        });
    }

    private function row(string $version): PhpVersion
    {
        return PhpVersion::where('version', $version)->firstOrFail();
    }

    /** @return list<array{method: string, command: string, params: array<string, mixed>}> */
    private function submits(string $command): array
    {
        return array_values(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit' && $c['command'] === $command));
    }

    public function testListsInstalledVersions(): void
    {
        $this->server();

        Livewire::test(PhpVersions::class)
            ->assertOk()
            ->assertCanSeeTableRecords(PhpVersion::all())
            ->assertSee(['8.2', '8.3', '8.4', '8.5'])
            ->assertSee('Dec 31, 2026')
            ->assertSee(__('zimam.php.badges.default'))
            ->assertSee(__('zimam.php.badges.panel'));
    }

    public function testInstallSubmits(): void
    {
        Livewire::test(PhpVersions::class)
            ->callAction('install', ['version' => '8.3'])
            ->assertHasNoActionErrors();

        $this->assertSame([['version' => '8.3']], array_column($this->submits('php.install'), 'params'));
        $row = $this->row('8.3');
        $this->assertSame(PhpVersion::INSTALLING, $row->status);
        $job = AgentJob::where('command', 'php.install')->sole();
        $this->assertTrue($job->related->is($row));
        $this->assertSame(900, $job->runtime_max_seconds);
    }

    /** Only versions the agent lists as supported, and not already installed, are offered (D19). */
    public function testInstallOffersOnlySupported(): void
    {
        $this->travelTo(Carbon::parse('2027-01-02 09:00:00', 'UTC'));
        $this->app->instance(AgentClient::class, $this->agent = new FakeAgentClient); // its allowlist, as of today

        $page = Livewire::test(PhpVersions::class);
        $this->assertSame(['8.3', '8.4'], array_keys($page->instance()->installableVersions()));

        foreach (['8.2', '8.5'] as $version) {
            Livewire::test(PhpVersions::class)
                ->callAction('install', ['version' => $version])
                ->assertHasActionErrors(['version']);
        }
        $this->assertSame([], $this->submits('php.install'));
    }

    /** A refusal leaves no row behind and tells the user why. */
    public function testInstallRefusedRemovesRow(): void
    {
        $this->agent->respond('php.install', new AgentConflict('PHP 8.3 is already installed.'));

        Livewire::test(PhpVersions::class)
            ->callAction('install', ['version' => '8.3'])
            ->assertNotified('PHP 8.3 is already installed.');

        $this->assertNull(PhpVersion::where('version', '8.3')->first());
        $this->assertSame(0, AgentJob::count());
    }

    public function testUninstallRequiresPassword(): void
    {
        $this->server();
        Livewire::test(PhpVersions::class)
            ->callAction(TestAction::make('uninstall')->table($this->row('8.2')), ['current_password' => 'not the password'])
            ->assertHasActionErrors(['current_password']);
        $this->assertSame([], $this->submits('php.uninstall'));

        Livewire::test(PhpVersions::class)->callAction(TestAction::make('uninstall')->table($this->row('8.2')), ['current_password' => 'the right password'])
            ->assertHasNoActionErrors();
        $this->assertSame([['version' => '8.2']], array_column($this->submits('php.uninstall'), 'params'));
        $this->assertSame(PhpVersion::UNINSTALLING, $this->row('8.2')->status);
    }

    public function testUninstallDisabledForDefaultPanelAndUsed(): void
    {
        $this->server();

        Livewire::test(PhpVersions::class)
            ->assertActionEnabled(TestAction::make('uninstall')->table($this->row('8.2')))
            ->assertActionDisabled(TestAction::make('uninstall')->table($this->row('8.3')))
            ->assertActionDisabled(TestAction::make('uninstall')->table($this->row('8.4')))
            ->assertActionDisabled(TestAction::make('uninstall')->table($this->row('8.5')));

        // The reason is the disabled button's tooltip.
        $this->assertNull($this->row('8.2')->uninstallBlocker());
        $this->assertSame(__('zimam.php.uninstall.blocked.in_use', ['version' => '8.3']), $this->row('8.3')->uninstallBlocker());
        $this->assertSame(__('zimam.php.uninstall.blocked.default', ['version' => '8.4']), $this->row('8.4')->uninstallBlocker());
        $this->assertSame(__('zimam.php.uninstall.blocked.panel', ['version' => '8.5']), $this->row('8.5')->uninstallBlocker());
    }

    public function testSetDefaultSubmits(): void
    {
        $this->server();

        Livewire::test(PhpVersions::class)
            ->assertActionHidden(TestAction::make('setDefault')->table($this->row('8.4')))
            ->callAction(TestAction::make('setDefault')->table($this->row('8.3')))
            ->assertHasNoActionErrors();

        $this->assertSame([['version' => '8.3']], array_column($this->submits('php.default'), 'params'));
        $this->assertTrue(AgentJob::where('command', 'php.default')->sole()->related->is($this->row('8.3')));
    }

    /** Every action goes through AgentHealth::guard. */
    public function testActionsPausedWhileAgentUnreachable(): void
    {
        $this->server();
        Livewire::test(PhpVersions::class); // syncs the rows while the agent answers
        $this->agent->respond('agent.version', new AgentUnavailable('connect: no such file'));
        $this->travel(11)->seconds();
        // A new request, as on a server: AgentHealth keeps its answer for one request.
        $this->app->forgetScopedInstances();

        Livewire::test(PhpVersions::class)
            ->assertActionDisabled('install')
            ->assertActionDisabled(TestAction::make('uninstall')->table($this->row('8.2')))
            ->assertActionDisabled(TestAction::make('setDefault')->table($this->row('8.3')));
        $this->assertSame([], array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit'));
    }
}
