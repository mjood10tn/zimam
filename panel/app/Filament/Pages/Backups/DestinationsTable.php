<?php

namespace App\Filament\Pages\Backups;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\BackupDestination as BackupDestinationCommand;
use App\Agent\Commands\BackupDestinationDelete;
use App\Agent\Commands\BackupDestinationKeys;
use App\Agent\Commands\BackupDestinationTest;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\BackupDestination;
use App\Support\DisplayTime;
use Closure;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Components\View;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\View\View as ViewContract;
use Illuminate\Support\Facades\Crypt;
use Livewire\Attributes\Locked;
use Livewire\Component;
use SensitiveParameter;

/**
 * The Backups page's destinations: add one with its encryption keys shown
 * once (R05-D3), test it, show its keys again, delete it.
 *
 * The keys are made when the add dialog opens and kept, while it is open,
 * only sealed with the app key in $sealedKeys: the component's state never
 * holds them in clear, and the panel's database never holds them at all.
 * They go to the agent once, after the owner retyped the password's last
 * characters, and the seal is dropped. "Show keys again" reads them from
 * the agent into this one response's HTML, as RotatesServicePassword does
 * with a new password.
 */
class DestinationsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    /** The open add dialog's keys, encrypted with the app key; null when none is open. */
    #[Locked]
    public ?string $sealedKeys = null;

    /** Changes with each "Show keys again", so the browser swaps in the new block; null when none is shown. */
    #[Locked]
    public ?string $revealKey = null;

    /** @var array{key: string, encryption_password: string, encryption_salt: string}|null the keys read back, only in the response that read them */
    private ?array $revealed = null;

    /** @return array{password: string, salt: string}|null */
    private function keys(): ?array
    {
        return $this->sealedKeys === null ? null : json_decode(Crypt::decryptString($this->sealedKeys), true);
    }

    public function addAction(): Action
    {
        return AgentHealth::guard(
            Action::make('add')
                ->label(__('zimam.backups.destinations.add.label'))
                ->modalHeading(__('zimam.backups.destinations.add.heading'))
                ->modalSubmitActionLabel(__('zimam.backups.destinations.add.submit'))
                ->mountUsing(function (?Schema $schema): void {
                    $this->sealedKeys = Crypt::encryptString(json_encode([
                        'password' => BackupDestination::generateSecret(),
                        'salt' => BackupDestination::generateSecret(),
                    ]));
                    $schema?->fill(['type' => BackupDestination::S3, 'port' => 22]);
                })
                ->schema(fn (): array => [
                    View::make('filament.backups.new-keys')->viewData(fn (): array => ['keys' => $this->keys()]),
                    ...self::destinationFields(),
                    TextInput::make('check')
                        ->label(__('zimam.backups.destinations.add.check', ['count' => BackupDestination::CHECK_LENGTH]))
                        ->helperText(__('zimam.backups.destinations.add.check_help'))
                        ->required()
                        ->autocomplete('off')
                        ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                            $password = $this->keys()['password'] ?? '';
                            if (! is_string($value) || $password === '' || ! hash_equals(substr($password, -BackupDestination::CHECK_LENGTH), $value)) {
                                $fail(__('zimam.backups.destinations.add.check_wrong'));
                            }
                        }),
                ])
                ->action(fn (array $data) => $this->add($data)),
        );
    }

    /** @return list<\Filament\Schemas\Components\Component> */
    private static function destinationFields(): array
    {
        $s3 = fn (Get $get): bool => $get('type') === BackupDestination::S3;
        $sftp = fn (Get $get): bool => $get('type') === BackupDestination::SFTP;

        return [
            TextInput::make('key')
                ->label(__('zimam.backups.destinations.fields.key'))
                ->helperText(__('zimam.backups.key_help'))
                ->required()
                ->regex(BackupDestination::KEY_PATTERN)
                ->unique(BackupDestination::class, 'key')
                ->validationMessages(['regex' => __('zimam.backups.validation.key'), 'unique' => __('zimam.backups.validation.key_taken')]),
            Select::make('type')
                ->label(__('zimam.backups.destinations.fields.type'))
                ->options(array_combine(BackupDestination::TYPES, array_map(fn (string $t): string => __("zimam.backups.destinations.types.{$t}"), BackupDestination::TYPES)))
                ->required()
                ->in(BackupDestination::TYPES)
                ->live(),
            TextInput::make('endpoint')
                ->label(__('zimam.backups.destinations.fields.endpoint'))
                ->helperText(__('zimam.backups.destinations.fields.endpoint_help'))
                ->visible($s3)->required($s3)
                ->regex(BackupDestination::S3_ENDPOINT_PATTERN)
                ->maxLength(270)
                ->validationMessages(['regex' => __('zimam.backups.validation.endpoint')]),
            TextInput::make('region')
                ->label(__('zimam.backups.destinations.fields.region'))
                ->visible($s3)->required($s3)
                ->regex(BackupDestination::S3_REGION_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.region')]),
            TextInput::make('bucket')
                ->label(__('zimam.backups.destinations.fields.bucket'))
                ->visible($s3)->required($s3)
                ->regex(BackupDestination::S3_BUCKET_PATTERN)
                ->notRegex(BackupDestination::S3_BUCKET_BAD_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.bucket'), 'not_regex' => __('zimam.backups.validation.bucket')]),
            TextInput::make('prefix')
                ->label(__('zimam.backups.destinations.fields.prefix'))
                ->helperText(__('zimam.backups.destinations.fields.path_help'))
                ->visible($s3)
                ->maxLength(BackupDestination::PATH_MAX_LENGTH)
                ->regex(BackupDestination::PATH_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.path')]),
            TextInput::make('access_key_id')
                ->label(__('zimam.backups.destinations.fields.access_key_id'))
                ->visible($s3)->required($s3)
                ->autocomplete('off')
                ->regex(BackupDestination::S3_CREDENTIAL_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.credential')]),
            TextInput::make('secret_access_key')
                ->label(__('zimam.backups.destinations.fields.secret_access_key'))
                ->visible($s3)->required($s3)
                ->password()->revealable()
                ->autocomplete('new-password')
                ->regex(BackupDestination::S3_CREDENTIAL_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.credential')]),
            TextInput::make('host')
                ->label(__('zimam.backups.destinations.fields.host'))
                ->visible($sftp)->required($sftp)
                ->maxLength(253)
                ->regex(BackupDestination::SFTP_HOST_PATTERN)
                ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                    // An IP address must be in canonical form, as the agent's SFTPHost wants it.
                    if (is_string($value) && preg_match('/^[0-9a-f.:]+\z/', $value) === 1 && @inet_ntop((string) @inet_pton($value)) !== $value) {
                        $fail(__('zimam.backups.validation.host'));
                    }
                })
                ->validationMessages(['regex' => __('zimam.backups.validation.host')]),
            TextInput::make('port')
                ->label(__('zimam.backups.destinations.fields.port'))
                ->visible($sftp)->required($sftp)
                ->integer()->minValue(1)->maxValue(65535),
            TextInput::make('user')
                ->label(__('zimam.backups.destinations.fields.user'))
                ->visible($sftp)->required($sftp)
                ->regex(BackupDestination::SFTP_USER_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.user')]),
            TextInput::make('path')
                ->label(__('zimam.backups.destinations.fields.path'))
                ->helperText(__('zimam.backups.destinations.fields.path_help'))
                ->visible($sftp)
                ->maxLength(BackupDestination::PATH_MAX_LENGTH)
                ->regex(BackupDestination::PATH_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.path')]),
            TextInput::make('host_key_fingerprint')
                ->label(__('zimam.backups.destinations.fields.fingerprint'))
                ->helperText(__('zimam.backups.destinations.fields.fingerprint_help'))
                ->visible($sftp)
                ->regex(BackupDestination::FINGERPRINT_PATTERN)
                ->validationMessages(['regex' => __('zimam.backups.validation.fingerprint')]),
        ];
    }

    /**
     * Sends the new destination with its keys, never queued: a busy agent
     * or a refusal keeps the dialog open with the same keys to try again.
     *
     * @param  array<string, mixed>  $data
     */
    private function add(#[SensitiveParameter] array $data): void
    {
        $keys = $this->keys();
        if ($keys === null) {
            return;
        }
        $s3 = $data['type'] === BackupDestination::S3;
        $settings = $s3
            ? array_filter(['endpoint' => $data['endpoint'], 'region' => $data['region'], 'bucket' => $data['bucket'], 'prefix' => $data['prefix'] ?? ''], fn ($v): bool => $v !== '' && $v !== null)
            : array_filter(['host' => $data['host'], 'port' => (int) $data['port'], 'user' => $data['user'], 'path' => $data['path'] ?? ''], fn ($v): bool => $v !== '' && $v !== null);
        $sent = $s3
            ? $settings + ['access_key_id' => $data['access_key_id'], 'secret_access_key' => $data['secret_access_key']]
            : $settings + array_filter(['host_key_fingerprint' => $data['host_key_fingerprint'] ?? null]);
        $row = BackupDestination::create([
            'key' => $data['key'],
            'type' => $data['type'],
            'settings' => $settings,
            'host_key_fingerprint' => $s3 ? null : (($data['host_key_fingerprint'] ?? null) ?: null),
            'status' => BackupDestination::SAVING,
        ]);
        try {
            app(SubmitAgentCommand::class)->run(new BackupDestinationCommand($data['key'], $data['type'], $sent, $keys['password'], $keys['salt']), $row, waitWhenBusy: false);
        } catch (Halt $e) {
            $row->delete();

            throw $e;
        } catch (AgentException $e) {
            $row->delete();
            Notification::make()
                ->title($e instanceof AgentBusy ? __('zimam.backups.busy') : __('zimam.backups.destinations.add.refused'))
                ->body($e instanceof AgentBusy ? null : self::reason($e))
                ->warning()
                ->send();

            throw new Halt;
        }
        $this->sealedKeys = null;
        Notification::make()->title(__('zimam.backups.destinations.add.submitted', ['key' => $data['key']]))->body(__('zimam.backups.submitted_body'))->success()->send();
    }

    public function table(Table $table): Table
    {
        $idle = fn (BackupDestination $record): ?string => match (true) {
            $record->status !== BackupDestination::ACTIVE => __('zimam.backups.destinations.not_ready'),
            $record->hasUnfinishedJob() => __('zimam.backups.working'),
            default => null,
        };

        return $table
            ->query(BackupDestination::query())
            ->defaultSort('key')
            ->paginated(false)
            ->emptyStateHeading(__('zimam.backups.destinations.empty'))
            ->columns([
                TextColumn::make('key')
                    ->label(__('zimam.backups.destinations.columns.name'))
                    ->weight('bold')
                    ->description(fn (BackupDestination $record): string => $record->location()),
                TextColumn::make('type')
                    ->label(__('zimam.backups.destinations.columns.type'))
                    ->formatStateUsing(fn (string $state): string => __("zimam.backups.destinations.types.{$state}")),
                TextColumn::make('status')
                    ->label(__('zimam.backups.destinations.columns.status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => __("zimam.backups.status.{$state}"))
                    ->color(fn (string $state): string => match ($state) {
                        BackupDestination::FAILED => 'danger',
                        BackupDestination::ACTIVE => 'gray',
                        default => 'warning',
                    }),
                TextColumn::make('details')
                    ->label(__('zimam.backups.destinations.columns.details'))
                    ->state(fn (BackupDestination $record): array => array_values(array_filter([
                        $record->last_error,
                        $record->last_tested_at ? __('zimam.backups.destinations.tested', ['time' => DisplayTime::format($record->last_tested_at, 'j M Y, H:i')]) : __('zimam.backups.destinations.untested'),
                        $record->host_key_fingerprint ? __('zimam.backups.destinations.fingerprint', ['fingerprint' => $record->host_key_fingerprint]) : null,
                        $record->public_key ? __('zimam.backups.destinations.public_key', ['key' => $record->public_key]) : null,
                    ])))
                    ->listWithLineBreaks()
                    ->wrap(),
            ])
            ->recordActions([
                AgentHealth::guard(
                    Action::make('test')
                        ->label(__('zimam.backups.destinations.test.label'))
                        ->color('gray')
                        ->action(fn (BackupDestination $record) => $this->submit(new BackupDestinationTest($record->key), $record, __('zimam.backups.destinations.test.submitted', ['key' => $record->key]))),
                    $idle,
                ),
                AgentHealth::guard(
                    ConfirmsPassword::apply(
                        Action::make('keys')
                            ->label(__('zimam.backups.destinations.keys.label'))
                            ->color('gray')
                            ->modalHeading(fn (BackupDestination $record): string => __('zimam.backups.destinations.keys.heading', ['key' => $record->key]))
                            ->modalDescription(__('zimam.backups.destinations.keys.description'))
                            ->modalSubmitActionLabel(__('zimam.backups.destinations.keys.submit'))
                            ->action(fn (BackupDestination $record) => $this->showKeys($record)),
                    ),
                    fn (BackupDestination $record): ?string => $record->status === BackupDestination::ACTIVE ? null : __('zimam.backups.destinations.not_ready'),
                ),
                AgentHealth::guard(
                    ConfirmsPassword::apply(
                        Action::make('delete')
                            ->label(__('zimam.backups.destinations.delete.label'))
                            ->color('danger')
                            ->visible(fn (BackupDestination $record): bool => in_array($record->status, [BackupDestination::ACTIVE, BackupDestination::DELETING], true))
                            ->modalHeading(fn (BackupDestination $record): string => __('zimam.backups.destinations.delete.heading', ['key' => $record->key]))
                            ->modalDescription(__('zimam.backups.destinations.delete.description'))
                            ->modalSubmitActionLabel(__('zimam.backups.destinations.delete.submit'))
                            ->action(fn (BackupDestination $record) => $this->delete($record)),
                    ),
                    fn (BackupDestination $record): ?string => $record->plans()->exists() ? __('zimam.backups.destinations.delete.in_use') : $idle($record),
                ),
                // A save that failed left nothing on the server: only the row remains.
                Action::make('dismiss')
                    ->label(__('zimam.backups.dismiss'))
                    ->color('gray')
                    ->visible(fn (BackupDestination $record): bool => $record->status === BackupDestination::FAILED)
                    ->action(fn (BackupDestination $record) => $record->delete()),
            ]);
    }

    private function showKeys(BackupDestination $record): void
    {
        try {
            $data = app(AgentClient::class)->call(new BackupDestinationKeys($record->key))->data;
        } catch (AgentException $e) {
            Notification::make()->title(__('zimam.backups.destinations.keys.refused'))->body(self::reason($e))->danger()->send();

            return;
        }
        $this->revealed = ['key' => $record->key, 'encryption_password' => $data['encryption_password'], 'encryption_salt' => $data['encryption_salt']];
        $this->revealKey = bin2hex(random_bytes(8));
    }

    /** The keys are shown once: closing them removes them from the page. */
    public function closeKeys(): void
    {
        $this->revealKey = null;
    }

    private function delete(BackupDestination $record): void
    {
        $record->update(['status' => BackupDestination::DELETING, 'last_error' => null]);
        if (! $this->submit(new BackupDestinationDelete($record->key), $record, __('zimam.backups.destinations.delete.submitted', ['key' => $record->key]))) {
            $record->update(['status' => BackupDestination::ACTIVE]);
        }
    }

    /** Submits a job for $record; false, after saying why, when the agent refused it. */
    private function submit(AgentCommand $command, BackupDestination $record, string $submitted): bool
    {
        try {
            app(SubmitAgentCommand::class)->run($command, $record);
        } catch (Halt) {
            return false;
        } catch (AgentException $e) {
            Notification::make()->title(self::reason($e))->danger()->send();

            return false;
        }
        Notification::make()->title($submitted)->body(__('zimam.backups.submitted_body'))->success()->send();

        return true;
    }

    public static function reason(AgentException $e): string
    {
        return $e instanceof AgentValidationFailed && $e->fields() !== [] ? implode(' ', $e->fields()) : $e->getMessage();
    }

    public function render(): ViewContract
    {
        return view('filament.backups.destinations', ['revealed' => $this->revealed]);
    }
}
