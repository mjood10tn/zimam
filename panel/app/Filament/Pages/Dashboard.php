<?php

namespace App\Filament\Pages;

use App\Filament\Widgets\ServerSummary;
use App\Filament\Widgets\Warnings;
use Filament\Pages\Dashboard as BaseDashboard;

/** The bulletin first, then the server; side by side from the lg breakpoint (dashboard brief §6). */
class Dashboard extends BaseDashboard
{
    public function getWidgets(): array
    {
        return [Warnings::class, ServerSummary::class];
    }

    public function getColumns(): int|array
    {
        return ['default' => 1, 'lg' => 2];
    }
}
