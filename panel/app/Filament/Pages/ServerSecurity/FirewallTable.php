<?php

namespace App\Filament\Pages\ServerSecurity;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\AgentHealth;
use App\Agent\Commands\FirewallRules;
use App\Agent\Commands\FirewallStatus;
use App\Agent\Exceptions\AgentException;
use App\Filament\Actions\ConfirmsPassword;
use App\Filament\Pages\Backups\DestinationsTable;
use App\Models\FirewallRule;
use App\Models\GuardedChange;
use App\Support\IpEntry;
use Closure;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Exceptions\Halt;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\View\View;
use Livewire\Component;

/**
 * The Server security page's firewall (R06-D3): Zimam's rules, the locked
 * ones marked and read-only, and the rules Zimam didn't make listed apart.
 * The rows are what firewall.status last reported; every add, edit or
 * delete sends the owner's whole list as one guarded change, and the rows
 * follow only once the owner keeps it.
 */
class FirewallTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    /** @var list<array{action: string, proto: string, port: string, from: string, ipv6: bool}> UFW's other rules, as reported */
    public array $external = [];

    /** Whether UFW is on; null when the agent didn't answer and the rows are the last copy. */
    public ?bool $active = null;

    public function mount(): void
    {
        if (! app(AgentHealth::class)->isReachable()) {
            return;
        }
        try {
            $status = app(AgentClient::class)->call(new FirewallStatus)->data;
        } catch (AgentException) {
            return;
        }
        FirewallRule::syncFrom($status);
        $this->external = $status['external'];
        $this->active = $status['active'];
    }

    public function addAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('add')
                    ->label(__('zimam.server_security.firewall.add'))
                    ->modalHeading(__('zimam.server_security.firewall.add'))
                    ->modalSubmitActionLabel(__('zimam.server_security.firewall.save'))
                    ->fillForm(['action' => 'allow', 'proto' => 'tcp'])
                    ->action(fn (array $data) => $this->save(null, $data)),
                self::fields(null),
            ),
            fn (): ?string => GuardedChange::blocker(),
        );
    }

    /** @return list<\Filament\Schemas\Components\Component> */
    private static function fields(?FirewallRule $rule): array
    {
        return [
            TextInput::make('key')
                ->label(__('zimam.server_security.firewall.fields.key'))
                ->helperText(__('zimam.server_security.firewall.fields.key_help'))
                ->required()
                ->disabled($rule !== null)
                ->regex(FirewallRule::KEY_PATTERN)
                ->unique(FirewallRule::class, 'key', ignoreRecord: true)
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                    if (is_string($value) && FirewallRule::isLockedKey($value)) {
                        $fail(__('zimam.server_security.firewall.validation.key_locked'));
                    }
                })
                ->validationMessages(['regex' => __('zimam.server_security.firewall.validation.key'), 'unique' => __('zimam.server_security.firewall.validation.key_taken')]),
            Select::make('action')
                ->label(__('zimam.server_security.firewall.fields.action'))
                ->options(array_combine(FirewallRule::ACTIONS, array_map(fn (string $a): string => __("zimam.server_security.firewall.actions.{$a}"), FirewallRule::ACTIONS)))
                ->required(),
            Select::make('proto')
                ->label(__('zimam.server_security.firewall.fields.proto'))
                ->options(array_combine(FirewallRule::PROTOS, array_map('strtoupper', FirewallRule::PROTOS)))
                ->required(),
            TextInput::make('port_from')
                ->label(__('zimam.server_security.firewall.fields.port_from'))
                ->integer()->minValue(1)->maxValue(65535)
                ->required(),
            TextInput::make('port_to')
                ->label(__('zimam.server_security.firewall.fields.port_to'))
                ->helperText(__('zimam.server_security.firewall.fields.port_to_help'))
                ->integer()->minValue(1)->maxValue(65535)
                ->rule(fn (Get $get): Closure => function (string $attribute, mixed $value, Closure $fail) use ($get): void {
                    if ($value !== null && $value !== '' && (int) $value < (int) $get('port_from')) {
                        $fail(__('zimam.server_security.firewall.validation.port_order'));
                    }
                }),
            Textarea::make('from')
                ->label(__('zimam.server_security.firewall.fields.from'))
                ->helperText(__('zimam.server_security.firewall.fields.from_help'))
                ->rows(3)
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                    $lines = IpEntry::lines(is_string($value) ? $value : '');
                    if (count($lines) > FirewallRule::MAX_SOURCES) {
                        $fail(__('zimam.server_security.firewall.validation.too_many_sources'));

                        return;
                    }
                    foreach ($lines as $i => $line) {
                        if (($error = IpEntry::error($line)) !== null) {
                            $fail(__('zimam.server_security.firewall.validation.from', ['line' => $i + 1, 'error' => $error]));

                            return;
                        }
                    }
                }),
        ];
    }

    public function table(Table $table): Table
    {
        $editable = fn (FirewallRule $record): bool => ! $record->locked;

        return $table
            ->query(FirewallRule::query()->orderByDesc('locked')->orderBy('key'))
            ->paginated(false)
            ->emptyStateHeading(__('zimam.server_security.firewall.empty'))
            ->columns([
                TextColumn::make('key')
                    ->label(__('zimam.server_security.firewall.columns.name'))
                    ->weight('bold')
                    ->description(fn (FirewallRule $record): ?string => $record->locked ? __('zimam.server_security.firewall.locked') : null),
                TextColumn::make('action')
                    ->label(__('zimam.server_security.firewall.columns.action'))
                    ->formatStateUsing(fn (string $state): string => __("zimam.server_security.firewall.actions.{$state}")),
                TextColumn::make('port_from')
                    ->label(__('zimam.server_security.firewall.columns.ports'))
                    ->formatStateUsing(fn (FirewallRule $record): string => $record->ports().'/'.$record->proto),
                TextColumn::make('from')
                    ->label(__('zimam.server_security.firewall.columns.from'))
                    ->state(fn (FirewallRule $record): array => $record->from === [] ? [__('zimam.server_security.firewall.anywhere')] : $record->from)
                    ->listWithLineBreaks()
                    ->limitList(3)
                    ->expandableLimitedList(),
            ])
            ->recordActions([
                AgentHealth::guard(
                    ConfirmsPassword::apply(
                        Action::make('edit')
                            ->label(__('zimam.server_security.firewall.edit'))
                            ->color('gray')
                            ->visible($editable)
                            ->modalHeading(fn (FirewallRule $record): string => __('zimam.server_security.firewall.edit_heading', ['key' => $record->key]))
                            ->modalSubmitActionLabel(__('zimam.server_security.firewall.save'))
                            ->fillForm(fn (FirewallRule $record): array => [
                                'key' => $record->key, 'action' => $record->action, 'proto' => $record->proto, 'port_from' => $record->port_from,
                                'port_to' => $record->port_to === $record->port_from ? null : $record->port_to, 'from' => implode("\n", $record->from),
                            ])
                            ->action(fn (FirewallRule $record, array $data) => $this->save($record, $data + ['key' => $record->key])),
                        self::fields(new FirewallRule),
                    ),
                    fn (): ?string => GuardedChange::blocker(),
                ),
                AgentHealth::guard(
                    ConfirmsPassword::apply(
                        Action::make('delete')
                            ->label(__('zimam.server_security.firewall.delete.label'))
                            ->color('danger')
                            ->visible($editable)
                            ->modalHeading(fn (FirewallRule $record): string => __('zimam.server_security.firewall.delete.heading', ['key' => $record->key]))
                            ->modalDescription(__('zimam.server_security.firewall.delete.description'))
                            ->modalSubmitActionLabel(__('zimam.server_security.firewall.delete.label'))
                            ->action(fn (FirewallRule $record) => $this->submit($this->ownRules($record->key))),
                    ),
                    fn (): ?string => GuardedChange::blocker(),
                ),
            ]);
    }

    /**
     * The owner's rules as they stand, less $without.
     *
     * @return list<array<string, mixed>>
     */
    private function ownRules(?string $without = null): array
    {
        return FirewallRule::where('locked', false)->where('key', '!=', (string) $without)->orderBy('key')->get()->map->spec()->values()->all();
    }

    /** @param array<string, mixed> $data */
    private function save(?FirewallRule $rule, array $data): void
    {
        $from = (int) $data['port_from'];
        $rules = $this->ownRules($rule?->key);
        $rules[] = [
            'id' => $data['key'],
            'action' => $data['action'],
            'proto' => $data['proto'],
            'port_from' => $from,
            'port_to' => filled($data['port_to'] ?? null) ? (int) $data['port_to'] : $from,
            'from' => IpEntry::lines($data['from'] ?? ''),
        ];
        if (count($rules) > FirewallRule::MAX_RULES) {
            Notification::make()->title(__('zimam.server_security.firewall.validation.too_many_rules'))->danger()->send();

            return;
        }
        $this->submit($rules);
    }

    /** @param list<array<string, mixed>> $rules */
    private function submit(array $rules): void
    {
        try {
            app(SubmitAgentCommand::class)->run(new FirewallRules($rules));
        } catch (Halt $e) {
            throw $e;
        } catch (AgentException $e) {
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            return;
        }
        Notification::make()->title(__('zimam.server_security.firewall.submitted'))->success()->send();
        $this->dispatch('guarded-change-started');
    }

    public function render(): View
    {
        return view('filament.server-security.firewall');
    }
}
