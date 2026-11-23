<?php

namespace App\Filament\Pages;

use App\Models\NotificationChannel;
use App\Notifications\PanelEvent;
use App\Settings\PanelSettings;
use App\Support\SmtpHost;
use BackedEnum;
use Closure;
use Filament\Actions\Action;
use Filament\Forms\Components\CheckboxList;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use SensitiveParameter;
use Throwable;

/**
 * Notifications (R05-D11): the SMTP server email goes through, and the
 * channels (email, Telegram, Slack, Discord) with the events each gets and
 * a test button. Tokens and webhook URLs are encrypted at rest and never
 * shown again; to change one, delete the channel and add it anew.
 */
class Notifications extends Page implements HasTable
{
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedBell;

    protected static ?int $navigationSort = 10;

    public static function getNavigationLabel(): string
    {
        return __('zimam.notify.title');
    }

    public function getTitle(): string
    {
        return __('zimam.notify.title');
    }

    public function content(Schema $schema): Schema
    {
        return $schema->components([
            Text::make(fn (): string => ($host = app(PanelSettings::class)->smtp()['host'] ?? '') === ''
                ? __('zimam.notify.smtp.none')
                : __('zimam.notify.smtp.using', ['host' => $host])),
            EmbeddedTable::make(),
        ]);
    }

    /** @return array<string, string> */
    private static function eventOptions(): array
    {
        return array_combine(NotificationChannel::EVENTS, array_map(fn (string $e): string => __("zimam.notify.event_names.{$e}"), NotificationChannel::EVENTS));
    }

    protected function getHeaderActions(): array
    {
        $is = fn (string ...$types): Closure => fn (Get $get): bool => in_array($get('type'), $types, true);

        return [
            Action::make('add')
                ->label(__('zimam.notify.add.label'))
                ->modalHeading(__('zimam.notify.add.label'))
                ->modalSubmitActionLabel(__('zimam.notify.add.submit'))
                ->fillForm(['type' => NotificationChannel::EMAIL, 'events' => NotificationChannel::EVENTS, 'enabled' => true])
                ->schema([
                    TextInput::make('name')->label(__('zimam.notify.fields.name'))->required()->maxLength(64),
                    Select::make('type')->label(__('zimam.notify.fields.type'))
                        ->options(array_combine(NotificationChannel::TYPES, array_map(fn (string $t): string => __("zimam.notify.types.{$t}"), NotificationChannel::TYPES)))
                        ->required()->in(NotificationChannel::TYPES)->live(),
                    TextInput::make('to')->label(__('zimam.notify.fields.to'))
                        ->visible($is(NotificationChannel::EMAIL))->required($is(NotificationChannel::EMAIL))->email()->maxLength(254),
                    TextInput::make('bot_token')->label(__('zimam.notify.fields.bot_token'))
                        ->visible($is(NotificationChannel::TELEGRAM))->required($is(NotificationChannel::TELEGRAM))
                        ->password()->revealable()->autocomplete('off')
                        ->regex(NotificationChannel::TELEGRAM_TOKEN_PATTERN)
                        ->validationMessages(['regex' => __('zimam.notify.validation.bot_token')]),
                    TextInput::make('chat_id')->label(__('zimam.notify.fields.chat_id'))
                        ->helperText(__('zimam.notify.fields.chat_id_help'))
                        ->visible($is(NotificationChannel::TELEGRAM))->required($is(NotificationChannel::TELEGRAM))
                        ->regex(NotificationChannel::TELEGRAM_CHAT_PATTERN)
                        ->validationMessages(['regex' => __('zimam.notify.validation.chat_id')]),
                    TextInput::make('webhook_url')->label(__('zimam.notify.fields.webhook_url'))
                        ->helperText(fn (Get $get): ?string => in_array($get('type'), [NotificationChannel::SLACK, NotificationChannel::DISCORD], true) ? __("zimam.notify.fields.webhook_help_{$get('type')}") : null)
                        ->visible($is(NotificationChannel::SLACK, NotificationChannel::DISCORD))
                        ->required($is(NotificationChannel::SLACK, NotificationChannel::DISCORD))
                        ->password()->revealable()->autocomplete('off')->maxLength(512)
                        ->regex(fn (Get $get): string => $get('type') === NotificationChannel::SLACK ? NotificationChannel::SLACK_URL_PATTERN : NotificationChannel::DISCORD_URL_PATTERN)
                        ->validationMessages(['regex' => __('zimam.notify.validation.webhook_url')]),
                    CheckboxList::make('events')->label(__('zimam.notify.fields.events'))->options(self::eventOptions())->in(NotificationChannel::EVENTS)->columns(2),
                    Toggle::make('enabled')->label(__('zimam.notify.fields.enabled')),
                ])
                ->action(fn (array $data) => $this->add($data)),
            Action::make('smtp')
                ->label(__('zimam.notify.smtp.label'))
                ->color('gray')
                ->modalHeading(__('zimam.notify.smtp.heading'))
                ->modalDescription(__('zimam.notify.smtp.description'))
                ->fillForm(fn (): array => ['security' => 'starttls', 'port' => 587] + array_diff_key(app(PanelSettings::class)->smtp(), ['password' => true]))
                ->schema([
                    TextInput::make('host')->label(__('zimam.notify.smtp.host'))->required()->maxLength(253)
                        ->regex('/^([A-Za-z0-9.-]+|\[?[0-9A-Fa-f:.]+\]?)\z/')
                        ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                            if (is_string($value) && ($refusal = app(SmtpHost::class)->refusal($value)) !== null) {
                                $fail($refusal);
                            }
                        }),
                    TextInput::make('port')->label(__('zimam.notify.smtp.port'))->required()->integer()->minValue(1)->maxValue(65535),
                    Select::make('security')->label(__('zimam.notify.smtp.security'))
                        ->options(['starttls' => __('zimam.notify.smtp.starttls'), 'tls' => __('zimam.notify.smtp.tls')])
                        ->required()->in(['starttls', 'tls']),
                    TextInput::make('username')->label(__('zimam.notify.smtp.username'))->maxLength(254)->autocomplete('off'),
                    TextInput::make('password')->label(__('zimam.notify.smtp.password'))
                        ->helperText(__('zimam.notify.smtp.password_help'))
                        ->password()->revealable()->maxLength(254)->autocomplete('new-password'),
                    TextInput::make('from')->label(__('zimam.notify.smtp.from'))->required()->email()->maxLength(254),
                ])
                ->action(function (#[SensitiveParameter] array $data): void {
                    $settings = app(PanelSettings::class);
                    $settings->setSmtp([
                        'host' => $data['host'],
                        'port' => (int) $data['port'],
                        'security' => $data['security'],
                        'username' => (string) ($data['username'] ?? ''),
                        'password' => ($data['password'] ?? '') !== '' ? $data['password'] : ($settings->smtp()['password'] ?? ''),
                        'from' => $data['from'],
                    ]);
                    Notification::make()->title(__('zimam.notify.smtp.saved'))->success()->send();
                }),
        ];
    }

    /** @param array<string, mixed> $data */
    private function add(#[SensitiveParameter] array $data): void
    {
        NotificationChannel::create([
            'type' => $data['type'],
            'name' => $data['name'],
            'settings' => match ($data['type']) {
                NotificationChannel::EMAIL => ['to' => $data['to']],
                NotificationChannel::TELEGRAM => ['bot_token' => $data['bot_token'], 'chat_id' => $data['chat_id']],
                default => ['webhook_url' => $data['webhook_url']],
            },
            'events' => array_values($data['events'] ?? []),
            'enabled' => (bool) ($data['enabled'] ?? true),
        ]);
        Notification::make()->title(__('zimam.notify.add.saved', ['name' => $data['name']]))->success()->send();
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(NotificationChannel::query())
            ->defaultSort('name')
            ->paginated(false)
            ->emptyStateHeading(__('zimam.notify.empty'))
            ->columns([
                TextColumn::make('name')
                    ->label(__('zimam.notify.fields.name'))
                    ->weight('bold')
                    ->description(fn (NotificationChannel $record): string => __("zimam.notify.types.{$record->type}")),
                TextColumn::make('events')
                    ->label(__('zimam.notify.fields.events'))
                    ->state(fn (NotificationChannel $record): array => array_map(fn (string $e): string => __("zimam.notify.event_names.{$e}"), $record->events))
                    ->listWithLineBreaks(),
                TextColumn::make('enabled')
                    ->label(__('zimam.notify.fields.state'))
                    ->formatStateUsing(fn (bool $state): string => $state ? __('zimam.notify.on') : __('zimam.notify.off'))
                    ->description(fn (NotificationChannel $record): ?string => $record->last_error === null ? null : __('zimam.notify.last_error', ['error' => $record->last_error]))
                    ->wrap(),
            ])
            ->recordActions([
                Action::make('test')
                    ->label(__('zimam.notify.test.label'))
                    ->color('gray')
                    ->action(fn (NotificationChannel $record) => $this->sendTest($record)),
                Action::make('edit')
                    ->label(__('zimam.notify.edit'))
                    ->color('gray')
                    ->fillForm(fn (NotificationChannel $record): array => $record->only(['name', 'events', 'enabled']))
                    ->schema([
                        TextInput::make('name')->label(__('zimam.notify.fields.name'))->required()->maxLength(64),
                        CheckboxList::make('events')->label(__('zimam.notify.fields.events'))->options(self::eventOptions())->in(NotificationChannel::EVENTS)->columns(2),
                        Toggle::make('enabled')->label(__('zimam.notify.fields.enabled')),
                    ])
                    ->action(fn (NotificationChannel $record, array $data) => $record->update(['name' => $data['name'], 'events' => array_values($data['events'] ?? []), 'enabled' => (bool) $data['enabled']])),
                Action::make('delete')
                    ->label(__('zimam.notify.delete'))
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(fn (NotificationChannel $record) => $record->delete()),
            ]);
    }

    /** Sends a test now, not queued, so the owner sees the provider's answer. */
    private function sendTest(NotificationChannel $channel): void
    {
        try {
            $channel->notifyNow(new PanelEvent('test', __('zimam.notify.test.title'), __('zimam.notify.test.body')));
        } catch (Throwable $e) {
            $channel->update(['last_error' => mb_substr($e->getMessage(), 0, 1000)]);
            Notification::make()->title(__('zimam.notify.test.failed'))->body($e->getMessage())->danger()->send();

            return;
        }
        $channel->update(['last_error' => null, 'last_sent_at' => now()]);
        Notification::make()->title(__('zimam.notify.test.sent'))->success()->send();
    }
}
