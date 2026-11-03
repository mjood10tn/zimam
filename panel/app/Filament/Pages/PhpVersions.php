<?php

namespace App\Filament\Pages;

use App\Actions\SubmitAgentCommand;
use App\Actions\SyncPhpVersions;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\PhpDefault;
use App\Agent\Commands\PhpInstall;
use App\Agent\Commands\PhpUninstall;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\ServerStatus;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\PhpVersion;
use BackedEnum;
use Carbon\CarbonImmutable;
use Closure;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\EmbeddedTable;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Filament\Support\Icons\Heroicon;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;

/**
 * The installed PHP versions with their end dates, the default and the
 * panel's version; install, uninstall and set default (§7.3, §8). The rows
 * are synced with system.status on every load.
 */
class PhpVersions extends Page implements HasTable
{
    use InteractsWithTable;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedCodeBracket;

    protected static ?int $navigationSort = 2;

    public static function getNavigationLabel(): string
    {
        return __('zimam.php.title');
    }

    public function getTitle(): string
    {
        return __('zimam.php.title');
    }

    public function mount(): void
    {
        if (app(AgentHealth::class)->isReachable()) {
            app(SyncPhpVersions::class)->run();
        }
    }

    public function content(Schema $schema): Schema
    {
        return $schema->components([EmbeddedTable::make()]);
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(PhpVersion::query())
            ->defaultSort('version', 'desc')
            ->paginated(false)
            ->emptyStateHeading(__('zimam.php.empty'))
            ->columns([
                TextColumn::make('version')
                    ->label(__('zimam.php.columns.version'))
                    ->weight('bold'),
                TextColumn::make('roles')
                    ->label(__('zimam.php.columns.roles'))
                    ->state(fn (PhpVersion $record): array => array_values(array_filter([
                        $record->is_default ? __('zimam.php.badges.default') : null,
                        $record->is_panel ? __('zimam.php.badges.panel') : null,
                    ])))
                    ->badge(),
                TextColumn::make('status')
                    ->label(__('zimam.php.columns.status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => __("zimam.php.status.{$state}"))
                    ->color(fn (string $state): string => match ($state) {
                        PhpVersion::FAILED => 'danger',
                        PhpVersion::INSTALLED => 'success',
                        default => 'warning',
                    }),
                TextColumn::make('eol_date')
                    ->label(__('zimam.php.columns.eol'))
                    ->date()
                    ->color(fn (PhpVersion $record): ?string => $record->eol_date?->isPast() ? 'danger' : null),
            ])
            ->recordActions([
                AgentHealth::guard(
                    Action::make('setDefault')
                        ->label(__('zimam.php.set_default.label'))
                        ->visible(fn (PhpVersion $record): bool => ! $record->is_default && $record->status === PhpVersion::INSTALLED)
                        ->requiresConfirmation()
                        ->modalHeading(fn (PhpVersion $record): string => __('zimam.php.set_default.heading', ['version' => $record->version]))
                        ->modalDescription(__('zimam.php.set_default.description'))
                        ->action(fn (PhpVersion $record) => $this->submit(new PhpDefault($record->version), $record, 'set_default')),
                ),
                AgentHealth::guard(
                    ConfirmsPassword::apply(
                        Action::make('uninstall')
                            ->label(__('zimam.php.uninstall.label'))
                            ->color('danger')
                            ->modalHeading(fn (PhpVersion $record): string => __('zimam.php.uninstall.heading', ['version' => $record->version]))
                            ->modalDescription(__('zimam.php.uninstall.description'))
                            ->modalSubmitActionLabel(__('zimam.php.uninstall.label'))
                            ->action(function (PhpVersion $record): void {
                                $record->update(['status' => PhpVersion::UNINSTALLING]);
                                $this->submit(new PhpUninstall($record->version), $record, 'uninstall', fn () => $record->update(['status' => PhpVersion::INSTALLED]));
                            }),
                    ),
                    fn (PhpVersion $record): ?string => $record->uninstallBlocker(),
                ),
            ]);
    }

    protected function getHeaderActions(): array
    {
        return [
            AgentHealth::guard(
                Action::make('install')
                    ->label(__('zimam.php.install.label'))
                    ->schema([
                        Select::make('version')
                            ->label(__('zimam.php.install.version'))
                            ->options(fn (): array => $this->installableVersions())
                            ->noOptionsMessage(__('zimam.php.install.none'))
                            ->required(),
                    ])
                    ->modalSubmitActionLabel(__('zimam.php.install.submit'))
                    ->action(fn (array $data) => $this->install($data['version'])),
            ),
        ];
    }

    /**
     * The versions the install form offers: those the agent lists as
     * supported today (D19), less those installed or being installed or
     * uninstalled. A failed install can be tried again.
     *
     * @return array<string, string> version => label
     */
    public function installableVersions(): array
    {
        $status = ServerStatus::current();
        if ($status === null) {
            return [];
        }
        $taken = [
            ...array_column($status->data['php']['installed'], 'version'),
            ...PhpVersion::whereIn('status', [PhpVersion::INSTALLED, PhpVersion::INSTALLING, PhpVersion::UNINSTALLING])->pluck('version'),
        ];
        $versions = [];
        foreach ($status->data['php']['available'] as $available) {
            if ($available['supported'] && ! in_array($available['version'], $taken, true)) {
                $versions[$available['version']] = __('zimam.php.install.option', [
                    'version' => $available['version'],
                    'end' => CarbonImmutable::parse($available['security_end'])->translatedFormat('M j, Y'),
                ]);
            }
        }

        return $versions;
    }

    private function install(string $version): void
    {
        $row = PhpVersion::firstOrNew(['version' => $version]);
        $previous = $row->exists ? $row->status : null;
        $row->fill(['status' => PhpVersion::INSTALLING])->save();
        $this->submit(new PhpInstall($version), $row, 'install', fn () => $previous === null ? $row->delete() : $row->update(['status' => $previous]));
    }

    /**
     * Submits a php.* job for $row. A refusal runs $undo and says why; an
     * unreachable agent runs $undo and halts, after SubmitAgentCommand has
     * told the user.
     */
    private function submit(AgentCommand $command, PhpVersion $row, string $action, ?Closure $undo = null): void
    {
        try {
            app(SubmitAgentCommand::class)->run($command, $row);
        } catch (Halt $e) {
            $undo?->__invoke();

            throw $e;
        } catch (AgentException $e) {
            $undo?->__invoke();
            Notification::make()
                ->title($e instanceof AgentValidationFailed && $e->fields() !== [] ? implode(' ', $e->fields()) : $e->getMessage())
                ->danger()
                ->send();

            return;
        }
        Notification::make()
            ->title(__("zimam.php.{$action}.submitted", ['version' => $row->version]))
            ->body(__('zimam.php.submitted_body'))
            ->success()
            ->send();
    }
}
