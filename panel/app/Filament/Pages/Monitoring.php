<?php

namespace App\Filament\Pages;

use App\Filament\Widgets\LiveMetrics;
use App\Filament\Widgets\MetricChart;
use App\Filament\Widgets\ServiceStates;
use BackedEnum;
use Filament\Pages\Dashboard as BaseDashboard;
use Filament\Support\Icons\Heroicon;

/**
 * Live values refreshed every 5 seconds, the service states and their
 * warnings, then charts for 1 hour, 24 hours or 7 days (§7.3, §7.5).
 */
class Monitoring extends BaseDashboard
{
    protected static string $routePath = 'monitoring';

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedChartBar;

    protected static ?int $navigationSort = 0;

    public static function getNavigationLabel(): string
    {
        return __('zimam.monitoring.title');
    }

    public function getTitle(): string
    {
        return __('zimam.monitoring.title');
    }

    public function getWidgets(): array
    {
        return [
            LiveMetrics::class,
            ServiceStates::class,
            ...array_map(fn (string $metric) => MetricChart::make(['metric' => $metric]), MetricChart::METRICS),
        ];
    }

    public function getColumns(): int|array
    {
        return ['default' => 1, 'lg' => 2];
    }
}
