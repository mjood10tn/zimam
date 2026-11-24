<?php

namespace Tests\Feature\ServerSecurity;

use App\Agent\ServerStatus;
use App\Filament\Pages\ServerSecurity;
use App\Filament\Pages\ServerSecurity\UpdatesSection;
use App\Filament\Widgets\Warnings;
use App\Models\AgentJob;
use App\Models\OsUpdateRun;
use App\Support\DisplayTime;
use Carbon\CarbonImmutable;
use Livewire\Livewire;
use Tests\Feature\Deployments\DeploymentsTestCase;

/** The Updates section and the dashboard's update and reboot warnings (R06-D12..D16). */
final class UpdatesTest extends DeploymentsTestCase
{
    private function server(array $changes): void
    {
        $this->agent->changeServer(fn (array $status): array => array_replace_recursive($status, $changes));
        ServerStatus::fresh();
    }

    public function testAutoToggleSubmits(): void
    {
        Livewire::test(UpdatesSection::class)
            ->fillForm(['enabled' => true, 'auto_reboot' => true, 'reboot_time' => '04:15'])
            ->callAction('saveAuto')
            ->assertNotified(__('zimam.server_security.updates.auto.submitted'));

        $this->assertSame([['enabled' => true, 'auto_reboot' => true, 'reboot_time' => '04:15']], $this->submitted('updates.auto'));
    }

    public function testAutoRebootNeedsTimeAndWarnsSitesGoDown(): void
    {
        Livewire::test(UpdatesSection::class)
            ->fillForm(['enabled' => true, 'auto_reboot' => true, 'reboot_time' => ''])
            ->callAction('saveAuto')
            ->assertHasErrors(['data.reboot_time']);
        $this->assertSame([], $this->submitted('updates.auto'));

        Livewire::test(UpdatesSection::class)
            ->fillForm(['enabled' => true, 'auto_reboot' => true])
            ->assertSee(__('zimam.server_security.updates.auto.reboot_help'));
    }

    public function testAutoRebootDisabledWhileAutoUpdatesOff(): void
    {
        Livewire::test(UpdatesSection::class)
            ->fillForm(['enabled' => false, 'auto_reboot' => true, 'reboot_time' => '04:15'])
            ->assertFormFieldIsDisabled('auto_reboot')
            ->callAction('saveAuto');

        $this->assertSame([['enabled' => false, 'auto_reboot' => false]], $this->submitted('updates.auto'));
    }

    public function testInstallNeedsPasswordConfirmation(): void
    {
        Livewire::test(UpdatesSection::class)
            ->callAction('install', ['scope' => 'security', 'current_password' => 'wrong'])
            ->assertHasActionErrors(['current_password']);
        $this->assertSame([], $this->submitted('updates.install'));
    }

    public function testInstallDefaultsToSecurity(): void
    {
        Livewire::test(UpdatesSection::class)
            ->mountAction('install')
            ->assertActionDataSet(['scope' => 'security']);
    }

    public function testAllScopeListsServicesThatRestart(): void
    {
        $this->agent->installMysql();
        ServerStatus::fresh();

        Livewire::test(UpdatesSection::class)
            ->mountAction('install')
            ->assertMountedActionModalSee(__('zimam.server_security.updates.install.all_restarts', ['services' => 'Nginx, PHP-FPM, MySQL']));
    }

    public function testRunSummaryShowsUpgradedAndRestarted(): void
    {
        Livewire::test(UpdatesSection::class)
            ->callAction('install', ['scope' => 'all', 'current_password' => self::PASSWORD])
            ->assertNotified(__('zimam.server_security.updates.install.submitted'));

        $this->assertSame([['scope' => 'all']], $this->submitted('updates.install'));
        $this->trackToTheEnd(AgentJob::sole());

        $run = OsUpdateRun::sole();
        $this->assertSame(OsUpdateRun::SUCCEEDED, $run->status);
        $this->assertSame('gzip', $run->upgraded[0]['package']);
        Livewire::test(UpdatesSection::class)
            ->assertSee('gzip')
            ->assertSee('nginx.service');
    }

    public function testCountsRefreshAfterInstall(): void
    {
        $this->server(['updates' => ['total' => 4, 'security' => 3]]);
        $this->assertSame(3, ServerStatus::current()->data['updates']['security']);

        Livewire::test(UpdatesSection::class)->callAction('install', ['scope' => 'security', 'current_password' => self::PASSWORD]);
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(0, ServerStatus::current()->data['updates']['security']);
    }

    public function testRebootNowWarnsSitesGoDown(): void
    {
        Livewire::test(UpdatesSection::class)
            ->mountAction('rebootNow')
            ->assertMountedActionModalSee(__('zimam.server_security.updates.reboot.now_description'))
            ->fillForm(['current_password' => self::PASSWORD])
            ->callMountedAction();

        $this->assertSame([['at' => 'now']], $this->submitted('system.reboot'));
    }

    public function testScheduledRebootShownAndCancellable(): void
    {
        Livewire::test(UpdatesSection::class)
            ->callAction('scheduleReboot', ['at' => '03:30', 'current_password' => self::PASSWORD]);
        $this->assertSame([['at' => '03:30']], $this->submitted('system.reboot'));
        $this->trackToTheEnd(AgentJob::sole());

        $at = CarbonImmutable::parse(ServerStatus::current()->data['reboot_scheduled']);
        Livewire::test(UpdatesSection::class)
            ->assertSee(__('zimam.server_security.updates.reboot.scheduled', ['time' => DisplayTime::format($at, 'j M H:i')]))
            ->assertActionVisible('cancelReboot')
            ->callAction('cancelReboot');
        $this->assertCount(1, $this->submitted('system.reboot.cancel'));
    }

    public function testWarningHasInstallButton(): void
    {
        $this->server(['updates' => ['total' => 4, 'security' => 3]]);

        Livewire::test(Warnings::class)
            ->assertSee(__('zimam.dashboard.warnings.security_updates.action'))
            ->assertSee(ServerSecurity::getUrl().'#updates');
    }

    public function testRebootRequiredWarningHasScheduleButton(): void
    {
        $this->server(['reboot_required' => true]);

        Livewire::test(Warnings::class)
            ->assertSee(__('zimam.dashboard.warnings.reboot.action'))
            ->assertSee(ServerSecurity::getUrl().'#updates');
    }
}
