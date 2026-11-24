<?php

namespace Tests\Feature\ServerSecurity;

use App\Agent\Commands\ChangeRevert;
use App\Agent\Commands\FirewallRules;
use App\Agent\Commands\FirewallStatus;
use App\Agent\Commands\SshSettings;
use App\Filament\Pages\ServerSecurity;
use App\Filament\Pages\ServerSecurity\FirewallTable;
use App\Filament\Pages\ServerSecurity\SshForm;
use App\Livewire\GuardedChangeModal;
use App\Models\AgentJob;
use App\Models\FirewallRule;
use App\Models\GuardedChange;
use App\Settings\PanelSettings;
use App\Support\DisplayTime;
use Filament\Actions\Testing\TestAction;
use Livewire\Livewire;
use Tests\Feature\Deployments\DeploymentsTestCase;

/** The Server security page's firewall and SSH sections and the guarded-change modal (R06-D1..D4). */
final class FirewallAndSshTest extends DeploymentsTestCase
{
    private const APP = ['id' => 'app', 'action' => 'allow', 'proto' => 'tcp', 'port_from' => 8080, 'port_to' => 8080, 'from' => ['203.0.113.0/24']];

    /** A pending change as a succeeded job leaves it. */
    private function pending(string $kind = GuardedChange::FIREWALL, int $seconds = 120): GuardedChange
    {
        // The firewall change adds app; before it the server has no rule of the owner's.
        $job = $this->agent->submit($kind === GuardedChange::SSH ? new SshSettings(false, 'no', [22], 6, 120) : new FirewallRules([self::APP]));
        $result = $this->agent->follow($job->id, 0, function (): void {})->result;

        return GuardedChange::create([
            'agent_change_id' => $result['change'], 'kind' => $kind, 'deadline' => now()->addSeconds($seconds),
            'status' => GuardedChange::PENDING, 'params' => $kind === GuardedChange::SSH ? ['password_auth' => false, 'root_login' => 'no', 'ports' => [22], 'max_auth_tries' => 6, 'login_grace_seconds' => 120] : ['rules' => [self::APP]],
            'created_by' => $this->user->id,
        ]);
    }

    public function testPageSyncsRulesFromTheAgent(): void
    {
        $this->agent->firewall([self::APP], [['action' => 'allow', 'proto' => 'udp', 'port' => '51820', 'from' => 'any', 'ipv6' => false]]);

        Livewire::test(ServerSecurity::class)->assertOk();
        Livewire::test(FirewallTable::class)
            ->assertSee('app')
            ->assertSee('203.0.113.0/24')
            ->assertSee(__('zimam.server_security.firewall.locked'))
            ->assertSee('51820');

        $this->assertSame(['app', 'http', 'https', 'ssh-22'], FirewallRule::orderBy('key')->pluck('key')->all());
    }

    public function testLockedRowsNotDeletable(): void
    {
        FirewallRule::syncFrom($this->agent->call(new FirewallStatus)->data);
        $this->agent->firewall([self::APP]);
        FirewallRule::syncFrom($this->agent->call(new FirewallStatus)->data);

        $table = Livewire::test(FirewallTable::class);
        foreach (FirewallRule::where('locked', true)->get() as $locked) {
            $table->assertActionHidden(TestAction::make('delete')->table($locked))
                ->assertActionHidden(TestAction::make('edit')->table($locked));
        }
        $table->assertActionVisible(TestAction::make('delete')->table(FirewallRule::where('key', 'app')->sole()));
    }

    public function testSaveRulesStartsGuardedChange(): void
    {
        Livewire::test(FirewallTable::class)
            ->callAction('add', [
                'key' => 'app', 'action' => 'allow', 'proto' => 'tcp', 'port_from' => 8080,
                'from' => "203.0.113.0/24\n2001:db8::7", 'current_password' => self::PASSWORD,
            ])
            ->assertHasNoFormErrors()
            ->assertNotified(__('zimam.server_security.firewall.submitted'));

        $this->assertSame([['rules' => [['id' => 'app', 'action' => 'allow', 'proto' => 'tcp', 'port_from' => 8080, 'port_to' => 8080, 'from' => ['203.0.113.0/24', '2001:db8::7']]]]], $this->submitted('firewall.rules'));
        $this->trackToTheEnd(AgentJob::sole());

        $change = GuardedChange::sole();
        $this->assertSame(GuardedChange::PENDING, $change->status);
        $this->assertSame(GuardedChange::FIREWALL, $change->kind);
        $this->assertTrue($change->deadline->isFuture());
        // The panel's copy waits for Keep.
        $this->assertFalse(FirewallRule::where('key', 'app')->exists());
    }

    public function testLockedNameRefused(): void
    {
        Livewire::test(FirewallTable::class)
            ->callAction('add', ['key' => 'ssh-2222', 'action' => 'allow', 'proto' => 'tcp', 'port_from' => 2222, 'current_password' => self::PASSWORD])
            ->assertHasFormErrors(['key']);
        $this->assertSame([], $this->submitted('firewall.rules'));
    }

    public function testKeepConfirms(): void
    {
        $change = $this->pending();

        Livewire::test(GuardedChangeModal::class)
            ->assertActionMounted('review')
            ->assertSee(__('zimam.server_security.change.heading.firewall'))
            ->callMountedAction()
            ->assertNotified(__('zimam.server_security.change.kept'));

        $this->assertSame(GuardedChange::CONFIRMED, $change->refresh()->status);
        $this->assertSame(['app', 'http', 'https', 'ssh-22'], FirewallRule::orderBy('key')->pluck('key')->all());
    }

    public function testKeepStoresSshSettings(): void
    {
        $this->pending(GuardedChange::SSH, 300);

        Livewire::test(GuardedChangeModal::class)
            ->assertSee(__('zimam.server_security.change.body.ssh'))
            ->callMountedAction();

        $this->assertFalse(app(PanelSettings::class)->ssh()['password_auth']);
        $this->assertSame('no', app(PanelSettings::class)->ssh()['root_login']);
    }

    public function testRevertNowReverts(): void
    {
        $change = $this->pending();

        Livewire::test(GuardedChangeModal::class)
            ->assertActionMounted('review')
            ->callMountedAction(['revert' => true])
            ->assertNotified(__('zimam.server_security.change.reverted'));

        $this->assertSame(GuardedChange::REVERTED, $change->refresh()->status);
        $this->assertSame(['change.revert'], array_values(array_map(fn (array $c): string => $c['command'], array_filter($this->agent->calls(), fn (array $c): bool => str_starts_with($c['command'], 'change.')))));
        $this->assertSame([], $this->agent->call(new FirewallStatus)->data['rules']);
    }

    public function testModalSurvivesReload(): void
    {
        $this->pending();

        Livewire::test(GuardedChangeModal::class)->assertActionMounted('review');
        // A fresh page load mounts it again.
        Livewire::test(GuardedChangeModal::class)->assertActionMounted('review');
    }

    public function testExpiredChangeShownReverted(): void
    {
        $change = $this->pending();
        $change->update(['deadline' => now()->subMinute()]);

        Livewire::test(GuardedChangeModal::class)
            ->assertActionNotMounted('review')
            ->assertNotified(__('zimam.server_security.change.expired', ['kind' => __('zimam.server_security.change.kinds.firewall'), 'time' => DisplayTime::format($change->created_at, 'H:i')]));

        $this->assertSame(GuardedChange::REVERTED, $change->refresh()->status);
    }

    public function testKeepAfterTheAgentRevertedSaysSo(): void
    {
        $change = $this->pending();
        $this->agent->call(new ChangeRevert($change->agent_change_id));

        Livewire::test(GuardedChangeModal::class)
            ->callMountedAction()
            ->assertNotified(__('zimam.server_security.change.gone'));

        $this->assertSame(GuardedChange::REVERTED, $change->refresh()->status);
    }

    public function testSaveDisabledWhileAChangeIsPending(): void
    {
        $this->pending();

        Livewire::test(FirewallTable::class)->assertActionDisabled('add');
        Livewire::test(SshForm::class)->assertActionDisabled('save');
    }

    public function testSshFormNeverOffersRootLoginYes(): void
    {
        $form = Livewire::test(SshForm::class);
        $options = array_keys($form->instance()->rootLoginOptions());

        $this->assertSame(['no', 'prohibit-password'], $options);
        $this->assertNotContains('yes', $options);
        $form->fillForm(['root_login' => 'yes'])->callAction('save', ['current_password' => self::PASSWORD])->assertHasErrors(['data.root_login']);
        $this->assertSame([], $this->submitted('ssh.settings'));
    }

    public function testSshSaveSubmits(): void
    {
        Livewire::test(SshForm::class)
            ->fillForm(['password_auth' => false, 'root_login' => 'no', 'ports' => '22', 'max_auth_tries' => 3, 'login_grace_seconds' => 30])
            ->callAction('save', ['current_password' => self::PASSWORD])
            ->assertNotified(__('zimam.server_security.ssh.submitted'));

        $this->assertSame([['password_auth' => false, 'root_login' => 'no', 'ports' => [22], 'max_auth_tries' => 3, 'login_grace_seconds' => 30]], $this->submitted('ssh.settings'));
    }

    public function testSshPortChangeWarns(): void
    {
        Livewire::test(SshForm::class)
            ->fillForm(['ports' => '22, 2222'])
            ->mountAction('save')
            ->assertMountedActionModalSee(__('zimam.server_security.ssh.save.ports'));

        Livewire::test(SshForm::class)
            ->mountAction('save')
            ->assertMountedActionModalDontSee(__('zimam.server_security.ssh.save.ports'));
    }
}
