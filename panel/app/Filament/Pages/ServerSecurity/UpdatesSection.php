<?php

namespace App\Filament\Pages\ServerSecurity;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\SystemReboot;
use App\Agent\Commands\SystemRebootCancel;
use App\Agent\Commands\UpdatesAuto;
use App\Agent\Commands\UpdatesInstall;
use App\Agent\Exceptions\AgentException;
use App\Agent\ServerStatus;
use App\Filament\Actions\ConfirmsPassword;
use App\Filament\Pages\Backups\DestinationsTable;
use App\Models\AgentJob;
use App\Models\OsUpdateRun;
use Carbon\CarbonImmutable;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Radio;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Illuminate\Contracts\View\View;
use Livewire\Component;

/**
 * Server security → Updates (R06-D12..D16): the pending counts, Ubuntu's
 * automatic security updates and their optional reboot, "Install updates
 * now" with its live log and past runs, and the reboot (now, at a time,
 * cancel). The counts and the reboot state are system.status's.
 *
 * @property-read Schema $form
 */
class UpdatesSection extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    public const TIME_PATTERN = '/^([01][0-9]|2[0-3]):[0-5][0-9]\z/';

    /** @var array<string, mixed> */
    public ?array $data = [];

    public function mount(): void
    {
        $auto = $this->status()['auto_updates'] ?? null;
        $this->form->fill([
            'enabled' => (bool) ($auto['enabled'] ?? false),
            'auto_reboot' => (bool) ($auto['auto_reboot'] ?? false),
            'reboot_time' => $auto['reboot_time'] ?? '03:30',
        ]);
    }

    /** @return array<string, mixed> system.status, or nothing while the agent never answered */
    public function status(): array
    {
        return ServerStatus::current()?->data ?? [];
    }

    public function form(Schema $schema): Schema
    {
        $rebooting = fn (Get $get): bool => (bool) $get('enabled') && (bool) $get('auto_reboot');

        return $schema->statePath('data')->components([
            Toggle::make('enabled')
                ->label(__('zimam.server_security.updates.auto.enabled'))
                ->helperText(__('zimam.server_security.updates.auto.enabled_help'))
                ->live(),
            // R06-D16: no effect while automatic updates are off.
            Toggle::make('auto_reboot')
                ->label(__('zimam.server_security.updates.auto.auto_reboot'))
                ->disabled(fn (Get $get): bool => ! $get('enabled'))
                ->live(),
            TextInput::make('reboot_time')
                ->label(__('zimam.server_security.updates.auto.reboot_time'))
                ->helperText(__('zimam.server_security.updates.auto.reboot_help'))
                ->visible($rebooting)
                ->required($rebooting)
                ->regex(self::TIME_PATTERN)
                ->validationMessages(['regex' => __('zimam.server_security.updates.validation.time')]),
        ]);
    }

    public function saveAutoAction(): Action
    {
        return AgentHealth::guard(
            Action::make('saveAuto')
                ->label(__('zimam.server_security.updates.auto.save'))
                ->color('gray')
                ->action(function (): void {
                    $data = $this->form->getState();
                    $enabled = (bool) ($data['enabled'] ?? false);
                    $reboot = $enabled && (bool) ($data['auto_reboot'] ?? false);
                    $this->submit(new UpdatesAuto($enabled, $reboot, $reboot ? $data['reboot_time'] : null), __('zimam.server_security.updates.auto.submitted'));
                }),
        );
    }

    /** The services an upgrade of all packages may restart, as installed here (OQ9). */
    public function restartingServices(): string
    {
        $services = [__('zimam.server_security.updates.services.nginx'), __('zimam.server_security.updates.services.php')];
        foreach (['mysql', 'valkey'] as $name) {
            if ($this->status()['services'][$name]['installed'] ?? false) {
                $services[] = __("zimam.server_security.updates.services.{$name}");
            }
        }
        if ($this->status()['node']['installed'] ?? false) {
            $services[] = __('zimam.server_security.updates.services.node');
        }

        return implode(', ', $services);
    }

    public function installAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('install')
                    ->label(__('zimam.server_security.updates.install.label'))
                    ->modalHeading(__('zimam.server_security.updates.install.heading'))
                    ->modalSubmitActionLabel(__('zimam.server_security.updates.install.label'))
                    ->fillForm(['scope' => 'security'])
                    ->action(fn (array $data) => $this->install($data['scope'])),
                [
                    Radio::make('scope')
                        ->label(__('zimam.server_security.updates.install.scope'))
                        ->options(array_combine(OsUpdateRun::SCOPES, array_map(fn (string $s): string => __("zimam.server_security.updates.install.scopes.{$s}"), OsUpdateRun::SCOPES)))
                        ->descriptions(fn (): array => [
                            'security' => __('zimam.server_security.updates.install.security_help'),
                            'all' => __('zimam.server_security.updates.install.all_restarts', ['services' => $this->restartingServices()]),
                        ])
                        ->required(),
                ],
            ),
            fn (): ?string => $this->installing() !== null ? __('zimam.server_security.updates.install.running') : null,
        );
    }

    /** The unfinished updates.install job, if one runs. */
    public function installing(): ?AgentJob
    {
        return AgentJob::where('command', 'updates.install')->whereNotIn('status', AgentJob::FINISHED)->latest('id')->first();
    }

    private function install(string $scope): void
    {
        $run = OsUpdateRun::create(['scope' => $scope, 'status' => OsUpdateRun::RUNNING, 'started_at' => now()]);
        try {
            $job = app(SubmitAgentCommand::class)->run(new UpdatesInstall($scope), $run);
        } catch (Halt $e) {
            $run->delete();

            throw $e;
        } catch (AgentException $e) {
            $run->delete();
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            return;
        }
        $run->update(['agent_job_id' => $job->id]);
        Notification::make()->title(__('zimam.server_security.updates.install.submitted'))->success()->send();
    }

    public function rebootNowAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('rebootNow')
                    ->label(__('zimam.server_security.updates.reboot.now'))
                    ->color('danger')
                    ->modalHeading(__('zimam.server_security.updates.reboot.now_heading'))
                    ->modalDescription(__('zimam.server_security.updates.reboot.now_description'))
                    ->modalSubmitActionLabel(__('zimam.server_security.updates.reboot.now'))
                    ->action(fn () => $this->submit(new SystemReboot('now'), __('zimam.server_security.updates.reboot.submitted'))),
            ),
        );
    }

    public function scheduleRebootAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('scheduleReboot')
                    ->label(__('zimam.server_security.updates.reboot.schedule'))
                    ->color('gray')
                    ->modalHeading(__('zimam.server_security.updates.reboot.schedule_heading'))
                    ->modalSubmitActionLabel(__('zimam.server_security.updates.reboot.schedule'))
                    ->fillForm(['at' => '03:30'])
                    ->action(fn (array $data) => $this->submit(new SystemReboot($data['at']), __('zimam.server_security.updates.reboot.submitted'))),
                [
                    TextInput::make('at')
                        ->label(__('zimam.server_security.updates.reboot.at'))
                        ->helperText(__('zimam.server_security.updates.reboot.at_help'))
                        ->required()
                        ->regex(self::TIME_PATTERN)
                        ->validationMessages(['regex' => __('zimam.server_security.updates.validation.time')]),
                ],
            ),
        );
    }

    public function cancelRebootAction(): Action
    {
        return AgentHealth::guard(
            Action::make('cancelReboot')
                ->label(__('zimam.server_security.updates.reboot.cancel'))
                ->color('gray')
                ->visible(fn (): bool => $this->scheduledReboot() !== null)
                ->action(fn () => $this->submit(new SystemRebootCancel, __('zimam.server_security.updates.reboot.cancelled'))),
        );
    }

    public function scheduledReboot(): ?CarbonImmutable
    {
        $at = $this->status()['reboot_scheduled'] ?? null;

        return is_string($at) ? CarbonImmutable::parse($at) : null;
    }

    private function submit(AgentCommand $command, string $submitted): void
    {
        try {
            app(SubmitAgentCommand::class)->run($command);
        } catch (Halt $e) {
            throw $e;
        } catch (AgentException $e) {
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            return;
        }
        Notification::make()->title($submitted)->success()->send();
    }

    public function render(): View
    {
        return view('filament.server-security.updates', [
            'runs' => OsUpdateRun::query()->latest('id')->limit(10)->get(),
        ]);
    }
}
