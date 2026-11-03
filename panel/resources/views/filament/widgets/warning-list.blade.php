@php
    use Filament\Support\Icons\Heroicon;
@endphp

{{-- One card per warning in force, critical first (dashboard brief §6); the dashboard and the Monitoring page share it. --}}
@if ($warnings !== [])
    <ul class="zimam-warnings" aria-label="{{ __('zimam.dashboard.warnings_label') }}">
        @foreach ($warnings as $warning)
            @php
                $critical = $warning['severity'] === 'critical';
            @endphp

            <li wire:key="warning-{{ $loop->index }}" @class(['zimam-warning', 'zimam-critical' => $critical])>
                <div class="zimam-warning-head">
                    <x-filament::icon
                        :icon="$critical ? Heroicon::OutlinedExclamationCircle : Heroicon::OutlinedExclamationTriangle"
                        class="zimam-icon"
                    />
                    <h2>{{ $warning['title'] }}</h2>
                </div>
                <p class="zimam-warning-meta">
                    <span class="zimam-severity">{{ __('zimam.dashboard.severity.'.$warning['severity']) }}</span>
                    <span class="zimam-age">{{ $warning['at'] }}</span>
                </p>
                <p class="zimam-detail">{{ $warning['detail'] }}</p>
            </li>
        @endforeach
    </ul>
@endif
