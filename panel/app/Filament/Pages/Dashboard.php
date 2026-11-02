<?php

namespace App\Filament\Pages;

use App\Filament\Widgets\ServerSummary;
use App\Filament\Widgets\Warnings;
use Filament\Pages\Dashboard as BaseDashboard;

/** The bulletin first, then the server; side by side from the lg breakpoint (dashboard brief §6). */
class Dashboard extends BaseDashboard
{
    /** The bulletin's headline is the page's heading, so Filament's "Dashboard" heading goes. */
    public function getHeading(): ?string
    {
        return null;
    }

    /** @return array<string> */
    public function getPageClasses(): array
    {
        return ['zimam-dashboard'];
    }

    public function getWidgets(): array
    {
        return [Warnings::class, ServerSummary::class];
    }

    public function getColumns(): int|array
    {
        return ['default' => 1, 'lg' => 2];
    }
}
