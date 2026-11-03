@php
    use App\Filament\Widgets\Warnings;

    $stale = $reading && ! $reading->fresh;
@endphp

{{-- The monitored services (§7.5), read every 5 seconds. --}}
<x-filament-widgets::widget>
    <section wire:poll.5s @class(['zimam-server', 'zimam-stale' => $stale]) aria-labelledby="zimam-services-heading">
        <div class="zimam-section-head">
            <h2 id="zimam-services-heading">{{ __('zimam.monitoring.services.heading') }}</h2>
            @if ($reading)
                <span class="zimam-age">
                    @if ($stale)
                        <s>{{ Warnings::time($reading->at) }}</s>
                    @else
                        {{ Warnings::time($reading->at) }}
                    @endif
                </span>
            @endif
        </div>

        @if ($services === [])
            <p class="zimam-note">{{ __('zimam.monitoring.services.none') }}</p>
        @else
            <dl class="zimam-facts">
                @foreach ($services as $service)
                    <dt wire:key="unit-{{ $loop->index }}"><code>{{ $service['unit'] }}</code></dt>
                    <dd>
                        <x-filament::badge :color="$service['active'] ? 'success' : ($service['failed'] ? 'danger' : 'warning')">
                            {{ $service['state'] }}
                        </x-filament::badge>
                    </dd>
                @endforeach
            </dl>
        @endif
    </section>
</x-filament-widgets::widget>
