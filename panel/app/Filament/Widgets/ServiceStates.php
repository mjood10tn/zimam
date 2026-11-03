<?php

namespace App\Filament\Widgets;

use App\Agent\MetricsReading;
use Filament\Widgets\Widget;
use Illuminate\Support\Facades\Lang;

/**
 * The monitored services' states (§7.5), read every 5 seconds from the
 * same system.metrics answer as LiveMetrics.
 */
class ServiceStates extends Widget
{
    protected static bool $isDiscovered = false;

    protected static ?bool $isLazyLoaded = false;

    protected string $view = 'filament.widgets.service-states';

    /** @return array<string, mixed> */
    protected function getViewData(): array
    {
        $reading = MetricsReading::current();

        return [
            'reading' => $reading,
            'services' => array_map(fn (array $service): array => [
                'unit' => $service['unit'],
                'active' => $service['active_state'] === 'active',
                'failed' => $service['active_state'] === 'failed',
                'state' => Lang::has("zimam.monitoring.services.states.{$service['active_state']}")
                    ? __("zimam.monitoring.services.states.{$service['active_state']}")
                    : $service['active_state'],
                'file_state' => $service['unit_file_state'],
            ], $reading?->data['services'] ?? []),
        ];
    }
}
