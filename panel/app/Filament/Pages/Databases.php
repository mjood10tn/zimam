<?php

namespace App\Filament\Pages;

use App\Actions\SubmitAgentCommand;
use App\Actions\SyncMysql;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\DbCreate;
use App\Agent\Commands\DbDrop;
use App\Agent\Commands\MysqlInstall;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\ServerStatus;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\Database;
use App\Models\ServerService;
use App\Models\Site;
use App\Support\Bytes;
use BackedEnum;
use Closure;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;

/**
 * MySQL: install it, and every database with its site and size; create
 * one for a site, delete one. The rows are synced with the agent on every
 * load, and the agent's list is the truth.
 */
class Databases extends Page implements HasTable
{
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedCircleStack;

    protected static ?int $navigationSort = 3;

    public static function getNavigationLabel(): string
    {
        return __('zimam.databases.title');
    }

    public function getTitle(): string
    {
        return __('zimam.databases.title');
    }

    public function mount(): void
    {
        if (app(AgentHealth::class)->isReachable()) {
            app(SyncMysql::class)->run();
        }
    }

    /** Whether MySQL is on the server, as the agent last said. */
    public function mysqlInstalled(): bool
    {
        $status = ServerStatus::current();

        return $status !== null
            ? $status->data['services']['mysql']['installed']
            : ServerService::named(ServerService::MYSQL)?->status === ServerService::INSTALLED;
    }

    public function content(Schema $schema): Schema
    {
        return $schema->components([
            Text::make(fn (): string => $this->mysqlInstalled() ? __('zimam.databases.sizes_note') : __('zimam.databases.not_installed')),
            EmbeddedTable::make(),
        ]);
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(Database::query()->with('site'))
            ->defaultSort('name')
            ->paginated(false)
            ->emptyStateHeading(__('zimam.databases.empty'))
            ->columns([
                TextColumn::make('name')
                    ->label(__('zimam.databases.columns.name'))
                    ->weight('bold')
                    ->description(fn (Database $record): ?string => $record->last_error),
                TextColumn::make('site.name')
                    ->label(__('zimam.databases.columns.site')),
                TextColumn::make('size_bytes')
                    ->label(__('zimam.databases.columns.size'))
                    ->formatStateUsing(fn (int $state): string => Bytes::format($state)),
                TextColumn::make('status')
                    ->label(__('zimam.databases.columns.status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => __("zimam.databases.status.{$state}"))
                    ->color(fn (string $state): string => match ($state) {
                        Database::FAILED => 'danger',
                        Database::ACTIVE => 'success',
                        default => 'warning',
                    }),
            ])
            ->recordActions([
                AgentHealth::guard(
                    ConfirmsPassword::apply(
                        Action::make('drop')
                            ->label(__('zimam.databases.drop.label'))
                            ->color('danger')
                            ->visible(fn (Database $record): bool => $record->status === Database::ACTIVE)
                            ->modalHeading(fn (Database $record): string => __('zimam.databases.drop.heading', ['name' => $record->name]))
                            ->modalDescription(__('zimam.databases.drop.description'))
                            ->modalSubmitActionLabel(__('zimam.databases.drop.submit'))
                            ->action(fn (Database $record) => $this->drop($record)),
                    ),
                    fn (Database $record): ?string => $record->site->hasUnfinishedJob() ? __('zimam.databases.drop.blocked') : null,
                ),
                // A create that failed left no database: only the row remains.
                Action::make('dismiss')
                    ->label(__('zimam.databases.dismiss.label'))
                    ->color('gray')
                    ->visible(fn (Database $record): bool => $record->status === Database::FAILED)
                    ->action(fn (Database $record) => $record->delete()),
            ]);
    }

    protected function getHeaderActions(): array
    {
        return [
            AgentHealth::guard(
                Action::make('install')
                    ->label(__('zimam.databases.install.label'))
                    ->visible(fn (): bool => ! $this->mysqlInstalled() && ServerService::named(ServerService::MYSQL)?->status !== ServerService::INSTALLING)
                    ->requiresConfirmation()
                    ->modalHeading(__('zimam.databases.install.heading'))
                    ->modalDescription(__('zimam.databases.install.description'))
                    ->modalSubmitActionLabel(__('zimam.databases.install.submit'))
                    ->action(fn () => $this->install()),
            ),
            AgentHealth::guard(
                Action::make('create')
                    ->label(__('zimam.databases.create.label'))
                    ->visible(fn (): bool => $this->mysqlInstalled())
                    ->modalHeading(__('zimam.databases.create.heading'))
                    ->modalSubmitActionLabel(__('zimam.databases.create.submit'))
                    ->schema([
                        Select::make('site')
                            ->label(__('zimam.databases.create.site'))
                            ->options(fn (): array => Site::where('status', Site::ACTIVE)->orderBy('name')->pluck('name', 'name')->all())
                            ->noOptionsMessage(__('zimam.databases.create.no_sites'))
                            ->required()
                            ->live()
                            ->in(fn (): array => Site::where('status', Site::ACTIVE)->pluck('name')->all())
                            ->validationMessages(['in' => __('zimam.databases.validation.site')]),
                        TextInput::make('suffix')
                            ->label(__('zimam.databases.create.suffix'))
                            ->helperText(__('zimam.databases.create.suffix_help'))
                            ->prefix(fn (Get $get): string => Database::prefixFor((string) ($get('site') ?: '…')))
                            ->required()
                            ->regex(Database::SUFFIX_PATTERN)
                            ->validationMessages(['regex' => __('zimam.databases.validation.suffix')])
                            ->rule(fn (Get $get): Closure => function (string $attribute, mixed $value, Closure $fail) use ($get): void {
                                if (! is_string($value) || ! is_string($get('site')) || $get('site') === '') {
                                    return;
                                }
                                $name = Database::prefixFor($get('site')).$value;
                                $error = Database::nameError($name);
                                if ($error === null && Database::where('name', $name)->where('status', '!=', Database::FAILED)->exists()) {
                                    $error = 'taken';
                                }
                                if ($error !== null) {
                                    $fail(__("zimam.databases.validation.{$error}"));
                                }
                            }),
                    ])
                    ->action(fn (array $data) => $this->create($data['site'], $data['suffix'])),
            ),
        ];
    }

    private function install(): void
    {
        $row = ServerService::firstOrNew(['name' => ServerService::MYSQL]);
        $previous = $row->exists ? $row->only(['status', 'last_error']) : null;
        $row->fill(['status' => ServerService::INSTALLING, 'last_error' => null])->save();
        $this->submit(
            new MysqlInstall,
            $row,
            __('zimam.databases.install.submitted'),
            fn () => $previous === null ? $row->delete() : $row->update($previous),
        );
    }

    private function create(string $siteName, string $suffix): void
    {
        $site = Site::where('name', $siteName)->firstOrFail();
        $name = Database::prefixFor($siteName).$suffix;
        // A failed create left a row behind: trying again takes it over.
        $row = Database::firstOrNew(['name' => $name]);
        $row->fill(['site_id' => $site->id, 'size_bytes' => 0, 'status' => Database::CREATING, 'last_error' => null])->save();
        $this->submit(
            new DbCreate($siteName, $name),
            $row,
            __('zimam.databases.create.submitted', ['name' => $name]),
            fn () => $row->delete(),
        );
    }

    private function drop(Database $row): void
    {
        $row->update(['status' => Database::DROPPING, 'last_error' => null]);
        $this->submit(
            new DbDrop($row->site->name, $row->name),
            $row,
            __('zimam.databases.drop.submitted', ['name' => $row->name]),
            fn () => $row->update(['status' => Database::ACTIVE]),
        );
    }

    /**
     * Submits a job for $row. A refusal runs $undo and shows the agent's
     * words; an unreachable agent runs $undo and halts, after
     * SubmitAgentCommand has told the user.
     */
    private function submit(AgentCommand $command, ServerService|Database $row, string $submitted, Closure $undo): void
    {
        try {
            app(SubmitAgentCommand::class)->run($command, $row);
        } catch (Halt $e) {
            $undo();

            throw $e;
        } catch (AgentException $e) {
            $undo();
            Notification::make()
                ->title($e instanceof AgentValidationFailed && $e->fields() !== [] ? implode(' ', $e->fields()) : $e->getMessage())
                ->danger()
                ->send();

            return;
        }
        Notification::make()->title($submitted)->body(__('zimam.databases.submitted_body'))->success()->send();
    }
}
