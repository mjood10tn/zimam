<?php

namespace App\Filament\Pages\Backups;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\BackupRestore;
use App\Agent\Exceptions\AgentException;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\BackupRun;
use App\Models\Database;
use App\Models\Site;
use App\Support\Bytes;
use App\Support\DisplayTime;
use Closure;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Exceptions\Halt;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Contracts\View\View;
use Illuminate\Database\Eloquent\Builder;
use Livewire\Attributes\Locked;
use Livewire\Component;

/**
 * Backup runs, newest first: status, size, log and restore (R05-D10). A
 * database part restores into a new database of its site, or with the
 * owner's password over an existing one; a files part restores into
 * ~/restores/<backup>/ of its site (OQ6). On a site's Backups tab only the
 * runs of plans that include the site are listed.
 */
class RunsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    #[Locked]
    public ?Site $site = null;

    /**
     * The site a part restores into: a files part's own site; for a database
     * part, the site that owns the database now, or by the longest name
     * prefix when it is gone (the agent decides; R03-D1).
     */
    public static function siteOf(string $part): ?Site
    {
        if (str_starts_with($part, 'files-')) {
            return Site::where('name', substr($part, 6))->first();
        }
        $name = substr($part, 3);
        if (($database = Database::where('name', $name)->first()) !== null) {
            return $database->site;
        }

        return Site::all()
            ->filter(fn (Site $site): bool => str_starts_with($name, Database::prefixFor($site->name)))
            ->sortByDesc(fn (Site $site): int => strlen($site->name))
            ->first();
    }

    /** @return array<string, string> the parts as options, labelled in words */
    private static function partOptions(BackupRun $run, bool $databasesOnly = false): array
    {
        $options = [];
        foreach ($run->restorableParts() as $part) {
            if (! $databasesOnly || str_starts_with($part, 'db-')) {
                $options[$part] = str_starts_with($part, 'db-')
                    ? __('zimam.backups.restore.part_database', ['name' => substr($part, 3)])
                    : __('zimam.backups.restore.part_files', ['name' => substr($part, 6)]);
            }
        }

        return $options;
    }

    public function table(Table $table): Table
    {
        $restorable = fn (BackupRun $record): bool => $record->status === BackupRun::SUCCEEDED && $record->restorableParts() !== [];

        return $table
            ->query(BackupRun::query()->with('plan')->when($this->site !== null, fn (Builder $q) => $q->whereHas('plan.sites', fn (Builder $s) => $s->whereKey($this->site->id))))
            ->defaultSort('backup_id', 'desc')
            ->paginated([10, 25, 50])
            ->emptyStateHeading(__('zimam.backups.runs.empty'))
            ->columns([
                TextColumn::make('plan.key')
                    ->label(__('zimam.backups.runs.columns.plan'))
                    ->weight('bold')
                    ->description(fn (BackupRun $record): string => __("zimam.backups.runs.trigger.{$record->trigger}")),
                TextColumn::make('status')
                    ->label(__('zimam.backups.runs.columns.status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => __("zimam.backups.runs.status.{$state}"))
                    ->color(fn (string $state): string => match ($state) {
                        BackupRun::FAILED => 'danger',
                        BackupRun::RUNNING => 'warning',
                        default => 'gray',
                    })
                    ->description(fn (BackupRun $record): ?string => $record->error)
                    ->wrap(),
                TextColumn::make('size_bytes')
                    ->label(__('zimam.backups.runs.columns.size'))
                    ->formatStateUsing(fn (int $state): string => Bytes::format($state)),
                TextColumn::make('started_at')
                    ->label(__('zimam.backups.runs.columns.started'))
                    ->formatStateUsing(fn ($state): string => DisplayTime::format($state, 'j M Y, H:i'))
                    ->alignEnd(),
            ])
            ->recordActions([
                Action::make('log')
                    ->label(__('zimam.backups.runs.log'))
                    ->color('gray')
                    ->modalHeading(fn (BackupRun $record): string => __('zimam.backups.runs.log_heading', ['plan' => $record->plan->key, 'backup' => $record->backup_id]))
                    ->modalContent(fn (BackupRun $record): View => view('filament.backups.run-log', ['log' => $record->logTail()]))
                    ->modalSubmitAction(false),
                AgentHealth::guard(
                    Action::make('restore')
                        ->label(__('zimam.backups.restore.label'))
                        ->visible($restorable)
                        ->modalHeading(fn (BackupRun $record): string => __('zimam.backups.restore.heading', ['backup' => $record->backup_id]))
                        ->modalDescription(__('zimam.backups.restore.description'))
                        ->modalSubmitActionLabel(__('zimam.backups.restore.submit'))
                        ->schema(fn (BackupRun $record): array => [
                            Select::make('part')
                                ->label(__('zimam.backups.restore.part'))
                                ->options(self::partOptions($record))
                                ->required()
                                ->in($record->restorableParts())
                                ->live(),
                            TextInput::make('suffix')
                                ->label(__('zimam.backups.restore.new_database'))
                                ->helperText(__('zimam.backups.restore.new_database_help'))
                                ->prefix(fn (Get $get): string => Database::prefixFor(self::siteOf((string) $get('part'))?->name ?? '…'))
                                ->visible(fn (Get $get): bool => str_starts_with((string) $get('part'), 'db-'))
                                ->required(fn (Get $get): bool => str_starts_with((string) $get('part'), 'db-'))
                                ->regex(Database::SUFFIX_PATTERN)
                                ->validationMessages(['regex' => __('zimam.databases.validation.suffix')])
                                ->rule(fn (Get $get): Closure => function (string $attribute, mixed $value, Closure $fail) use ($get): void {
                                    $site = self::siteOf((string) $get('part'));
                                    if (! is_string($value) || $site === null) {
                                        return;
                                    }
                                    $name = Database::prefixFor($site->name).$value;
                                    $error = Database::nameError($name) ?? (Database::where('name', $name)->exists() ? 'taken' : null);
                                    if ($error !== null) {
                                        $fail(__("zimam.databases.validation.{$error}"));
                                    }
                                }),
                            Text::make(fn (Get $get): string => str_starts_with((string) $get('part'), 'files-')
                                ? __('zimam.backups.restore.folder', ['folder' => '/home/s_'.substr((string) $get('part'), 6).'/restores/'.$record->backup_id.'/'])
                                : ''),
                        ])
                        ->action(function (BackupRun $record, array $data): void {
                            $site = self::siteOf($data['part']);
                            $database = str_starts_with($data['part'], 'db-') && $site !== null ? Database::prefixFor($site->name).$data['suffix'] : null;
                            $this->restore($record, $data['part'], $site, $database, false);
                        }),
                ),
                AgentHealth::guard(
                    ConfirmsPassword::apply(
                        Action::make('overwrite')
                            ->label(__('zimam.backups.restore.overwrite_label'))
                            ->color('danger')
                            ->visible(fn (BackupRun $record): bool => $restorable($record) && self::partOptions($record, true) !== [])
                            ->modalHeading(fn (BackupRun $record): string => __('zimam.backups.restore.overwrite_heading', ['backup' => $record->backup_id]))
                            ->modalDescription(__('zimam.backups.restore.overwrite_description'))
                            ->modalSubmitActionLabel(__('zimam.backups.restore.overwrite_submit'))
                            ->action(fn (BackupRun $record, array $data) => $this->restore($record, $data['part'], self::siteOf($data['part']), $data['database'], true)),
                        [
                            Select::make('part')
                                ->label(__('zimam.backups.restore.part'))
                                ->options(fn (BackupRun $record): array => self::partOptions($record, true))
                                ->required()
                                ->in(fn (BackupRun $record): array => array_keys(self::partOptions($record, true)))
                                ->live(),
                            Select::make('database')
                                ->label(__('zimam.backups.restore.existing_database'))
                                ->options(fn (Get $get): array => ($site = self::siteOf((string) $get('part'))) === null ? [] : $site->databases()->where('status', Database::ACTIVE)->orderBy('name')->pluck('name', 'name')->all())
                                ->in(fn (Get $get): array => ($site = self::siteOf((string) $get('part'))) === null ? [] : $site->databases()->where('status', Database::ACTIVE)->pluck('name')->all())
                                ->required(),
                        ],
                    ),
                ),
            ]);
    }

    private function restore(BackupRun $run, string $part, ?Site $site, ?string $database, bool $overwrite): void
    {
        if ($site === null) {
            Notification::make()->title(__('zimam.backups.restore.no_site'))->warning()->send();

            return;
        }
        if (($blocker = $site->jobBlocker()) !== null) {
            Notification::make()->title($blocker)->warning()->send();

            return;
        }
        try {
            app(SubmitAgentCommand::class)->run(new BackupRestore($run->plan->key, $run->backup_id, $part, $site->name, $database, $overwrite), $site);
        } catch (Halt) {
            return;
        } catch (AgentException $e) {
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            return;
        }
        Notification::make()->title(__('zimam.backups.restore.submitted'))->body(__('zimam.backups.restore.submitted_body', ['site' => $site->name]))->success()->send();
    }

    public function render(): View
    {
        return view('filament.backups.runs');
    }
}
