<?php

namespace App\Filament\Pages\ServerSecurity;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\Fail2banConfig;
use App\Agent\Commands\Fail2banStatus;
use App\Agent\Commands\Fail2banUnban;
use App\Agent\Exceptions\AgentException;
use App\Filament\Actions\ConfirmsPassword;
use App\Filament\Pages\Backups\DestinationsTable;
use App\Models\Fail2banJail;
use App\Settings\PanelSettings;
use App\Support\IpEntry;
use Closure;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Fieldset;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Illuminate\Contracts\View\View;
use Livewire\Component;

/**
 * The Server security page's fail2ban (R06-D5): the three jails, the
 * addresses never banned (first offered as the owner's own, so a few wrong
 * passwords can't lock them out), and the bans with Unban. Times are shown
 * in minutes and sent in seconds. In Cloudflare mode the jails stay on, and
 * the section says what a ban still stops (OQ5).
 *
 * @property-read Schema $form
 */
class Fail2banForm extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    /** @var array<string, mixed> */
    public ?array $data = [];

    /** @var array{installed: bool, running: bool, jails: list<array{name: string, banned: list<array{address: string, until: string}>}>}|null fail2ban.status, or null when the agent didn't answer */
    public ?array $status = null;

    public function mount(): void
    {
        if (app(AgentHealth::class)->isReachable()) {
            try {
                $this->status = app(AgentClient::class)->call(new Fail2banStatus)->data;
            } catch (AgentException) {
                // The section says the state is unknown.
            }
        }
        $jails = [];
        foreach (Fail2banJail::settings() as $name => $s) {
            $jails[$name] = ['enabled' => $s['enabled'], 'max_retry' => $s['max_retry'], 'find_minutes' => intdiv($s['find_time'], 60), 'ban_minutes' => intdiv($s['ban_time'], 60)];
        }
        $ignore = app(PanelSettings::class)->fail2banIgnore() ?? [request()->ip()];
        $this->form->fill(['jails' => $jails, 'ignore' => implode("\n", $ignore)]);
    }

    public function form(Schema $schema): Schema
    {
        $jails = [];
        foreach (array_keys(Fail2banJail::DEFAULTS) as $name) {
            $minutes = fn (string $field, array $seconds): TextInput => TextInput::make("jails.{$name}.{$field}")
                ->label(__("zimam.server_security.fail2ban.fields.{$field}"))
                ->integer()->minValue(intdiv($seconds[0], 60))->maxValue(intdiv($seconds[1], 60))
                ->required();
            $jails[] = Fieldset::make(__("zimam.server_security.fail2ban.jails.{$name}"))
                ->columns(['default' => 1, 'sm' => 2])
                ->schema([
                    Toggle::make("jails.{$name}.enabled")->label(__('zimam.server_security.fail2ban.fields.enabled'))->columnSpanFull(),
                    TextInput::make("jails.{$name}.max_retry")
                        ->label(__('zimam.server_security.fail2ban.fields.max_retry'))
                        ->integer()->minValue(1)->maxValue(Fail2banJail::MAX_RETRY)
                        ->required(),
                    $minutes('find_minutes', Fail2banJail::FIND_TIME),
                    $minutes('ban_minutes', Fail2banJail::BAN_TIME),
                ]);
        }

        return $schema->statePath('data')->components([
            ...$jails,
            Textarea::make('ignore')
                ->label(__('zimam.server_security.fail2ban.fields.ignore'))
                ->helperText(fn (): string => __('zimam.server_security.fail2ban.fields.ignore_help', ['address' => request()->ip()]))
                ->rows(3)
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                    $lines = IpEntry::lines(is_string($value) ? $value : '');
                    if (count($lines) > IpEntry::MAX_ENTRIES) {
                        $fail(__('zimam.server_security.fail2ban.validation.too_many'));

                        return;
                    }
                    foreach ($lines as $i => $line) {
                        if (($error = IpEntry::error($line)) !== null) {
                            $fail(__('zimam.server_security.fail2ban.validation.ignore', ['line' => $i + 1, 'error' => $error]));

                            return;
                        }
                    }
                }),
        ]);
    }

    public function cloudflareNote(): bool
    {
        return app(PanelSettings::class)->cloudflare()['enabled'];
    }

    public function saveAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('save')
                    ->label(fn (): string => __($this->status !== null && ! $this->status['installed'] ? 'zimam.server_security.fail2ban.save.install' : 'zimam.server_security.fail2ban.save.label'))
                    ->modalHeading(__('zimam.server_security.fail2ban.save.heading'))
                    ->modalDescription(__('zimam.server_security.fail2ban.save.description'))
                    ->modalSubmitActionLabel(__('zimam.server_security.fail2ban.save.label'))
                    ->action(function (): void {
                        $data = $this->form->getState();
                        $jails = [];
                        foreach (array_keys(Fail2banJail::DEFAULTS) as $name) {
                            $j = $data['jails'][$name];
                            $jails[] = ['name' => $name, 'enabled' => (bool) $j['enabled'], 'max_retry' => (int) $j['max_retry'], 'find_time' => 60 * (int) $j['find_minutes'], 'ban_time' => 60 * (int) $j['ban_minutes']];
                        }
                        $this->submit(new Fail2banConfig($jails, IpEntry::lines($data['ignore'] ?? '')), __('zimam.server_security.fail2ban.submitted'));
                    }),
            ),
        );
    }

    /** Arguments: the jail and the address, one of the bans fail2ban.status listed. */
    public function unbanAction(): Action
    {
        return AgentHealth::guard(
            Action::make('unban')
                ->label(__('zimam.server_security.fail2ban.unban.label'))
                ->color('gray')
                ->size('sm')
                ->requiresConfirmation()
                ->modalHeading(fn (array $arguments): string => __('zimam.server_security.fail2ban.unban.heading', ['address' => $arguments['address'] ?? '']))
                ->action(function (array $arguments): void {
                    $jail = (string) ($arguments['jail'] ?? '');
                    $address = (string) ($arguments['address'] ?? '');
                    if (! array_key_exists($jail, Fail2banJail::DEFAULTS) || IpEntry::error($address) !== null) {
                        return;
                    }
                    $this->submit(new Fail2banUnban($jail, $address), __('zimam.server_security.fail2ban.unban.submitted', ['address' => $address]));
                }),
        );
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
        return view('filament.server-security.fail2ban');
    }
}
