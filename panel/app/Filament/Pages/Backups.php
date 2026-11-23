<?php

namespace App\Filament\Pages;

use App\Actions\SyncBackupRuns;
use App\Agent\AgentHealth;
use App\Agent\Exceptions\AgentException;
use App\Filament\Pages\Backups\DestinationsTable;
use App\Filament\Pages\Backups\PlansTable;
use App\Filament\Pages\Backups\RunsTable;
use App\Models\BackupRun;
use BackedEnum;
use Filament\Pages\Page;
use Filament\Schemas\Components\Livewire;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Schema;
use Filament\Support\Icons\Heroicon;

/**
 * Backups (v0.5.0): destinations, plans and runs, each its own table
 * component. Loading the page reads the agent's newest run records first,
 * so a run that just ended shows without waiting for the minute's sync.
 */
class Backups extends Page
{
    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedArchiveBox;

    protected static ?int $navigationSort = 5;

    public static function getNavigationLabel(): string
    {
        return __('zimam.backups.title');
    }

    public function getTitle(): string
    {
        return __('zimam.backups.title');
    }

    public function mount(): void
    {
        if (app(AgentHealth::class)->isReachable()) {
            try {
                app(SyncBackupRuns::class)->run();
            } catch (AgentException) {
                // The minute's sync tries again.
            }
        }
    }

    public function content(Schema $schema): Schema
    {
        return $schema->components([
            Text::make(fn (): string => trans_choice('zimam.backups.lede', BackupRun::where('status', BackupRun::FAILED)->where('started_at', '>=', now()->subDay())->count())),
            Livewire::make(DestinationsTable::class)->key('backup-destinations'),
            Livewire::make(PlansTable::class)->key('backup-plans'),
            Livewire::make(RunsTable::class)->key('backup-runs'),
        ]);
    }
}
