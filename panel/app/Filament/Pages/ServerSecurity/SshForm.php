<?php

namespace App\Filament\Pages\ServerSecurity;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\SshSettings;
use App\Agent\Exceptions\AgentException;
use App\Filament\Actions\ConfirmsPassword;
use App\Filament\Pages\Backups\DestinationsTable;
use App\Models\GuardedChange;
use App\Settings\PanelSettings;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Illuminate\Contracts\View\View;
use Livewire\Component;

/**
 * The Server security page's SSH settings (R06-D4), a guarded change with
 * five minutes to keep it. Root login is never offered as "yes", and the
 * form shows what the panel last kept (PanelSettings::ssh()).
 *
 * @property-read Schema $form
 */
class SshForm extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    /** sshd accepts at most four of Zimam's ports (the agent's maxSSHPorts). */
    public const MAX_PORTS = 4;

    /** @var array<string, mixed> */
    public ?array $data = [];

    public function mount(): void
    {
        $ssh = app(PanelSettings::class)->ssh();
        $this->form->fill(['ports' => implode(', ', $ssh['ports'])] + $ssh);
    }

    /** @return array<string, string> the only two values the agent takes (R06-D4) */
    public function rootLoginOptions(): array
    {
        return [
            'no' => __('zimam.server_security.ssh.fields.root_login_options.no'),
            'prohibit-password' => __('zimam.server_security.ssh.fields.root_login_options.prohibit-password'),
        ];
    }

    public function form(Schema $schema): Schema
    {
        return $schema->statePath('data')->components([
            Toggle::make('password_auth')
                ->label(__('zimam.server_security.ssh.fields.password_auth'))
                ->helperText(__('zimam.server_security.ssh.fields.password_auth_help')),
            Select::make('root_login')
                ->label(__('zimam.server_security.ssh.fields.root_login'))
                ->options($this->rootLoginOptions())
                ->selectablePlaceholder(false)
                ->required(),
            TextInput::make('ports')
                ->label(__('zimam.server_security.ssh.fields.ports'))
                ->helperText(__('zimam.server_security.ssh.fields.ports_help'))
                ->required()
                ->live(onBlur: true)
                ->rule(fn () => function (string $attribute, mixed $value, \Closure $fail): void {
                    if (self::ports(is_string($value) ? $value : '') === null) {
                        $fail(__('zimam.server_security.ssh.validation.ports'));
                    }
                }),
            TextInput::make('max_auth_tries')
                ->label(__('zimam.server_security.ssh.fields.max_auth_tries'))
                ->integer()->minValue(1)->maxValue(10)
                ->required(),
            TextInput::make('login_grace_seconds')
                ->label(__('zimam.server_security.ssh.fields.login_grace_seconds'))
                ->integer()->minValue(10)->maxValue(600)
                ->required(),
        ]);
    }

    /**
     * "22, 2222" as distinct ports, or null when it isn't 1 to 4 of them.
     *
     * @return list<int>|null
     */
    public static function ports(string $text): ?array
    {
        $parts = array_map('trim', explode(',', $text));
        if (count($parts) > self::MAX_PORTS) {
            return null;
        }
        $ports = [];
        foreach ($parts as $part) {
            if (! preg_match('/^[1-9][0-9]{0,4}\z/', $part) || (int) $part > 65535 || in_array((int) $part, $ports, true)) {
                return null;
            }
            $ports[] = (int) $part;
        }

        return $ports;
    }

    private function portsChange(): bool
    {
        $ports = self::ports((string) ($this->data['ports'] ?? ''));

        return $ports !== null && $ports !== app(PanelSettings::class)->ssh()['ports'];
    }

    public function saveAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('save')
                    ->label(__('zimam.server_security.ssh.save.label'))
                    ->modalHeading(__('zimam.server_security.ssh.save.heading'))
                    ->modalDescription(fn (): string => __('zimam.server_security.ssh.save.description')
                        .($this->portsChange() ? ' '.__('zimam.server_security.ssh.save.ports') : ''))
                    ->modalSubmitActionLabel(__('zimam.server_security.ssh.save.label'))
                    ->action(function (): void {
                        // The form's own rules first: the modal only holds the password.
                        $data = $this->form->getState();
                        $this->submit(new SshSettings(
                            (bool) $data['password_auth'],
                            $data['root_login'],
                            self::ports($data['ports']),
                            (int) $data['max_auth_tries'],
                            (int) $data['login_grace_seconds'],
                        ));
                    }),
            ),
            fn (): ?string => GuardedChange::blocker(),
        );
    }

    private function submit(SshSettings $command): void
    {
        try {
            app(SubmitAgentCommand::class)->run($command);
        } catch (Halt $e) {
            throw $e;
        } catch (AgentException $e) {
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            return;
        }
        Notification::make()->title(__('zimam.server_security.ssh.submitted'))->success()->send();
        $this->dispatch('guarded-change-started');
    }

    public function render(): View
    {
        return view('filament.server-security.ssh');
    }
}
