@php
    use App\Filament\Widgets\Warnings;

    $stale = $reading && ! $reading->fresh;
@endphp

{{-- Live values, read every 5 seconds (§7.3, §7.5). --}}
<x-filament-widgets::widget>
    <div wire:poll.5s class="zimam-stack">
        <section @class(['zimam-server', 'zimam-stale' => $stale]) aria-labelledby="zimam-live-heading">
            <div class="zimam-section-head">
                <h2 id="zimam-live-heading">{{ __('zimam.monitoring.live.heading') }}</h2>
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

            @if ($reading === null)
                <p class="zimam-note">{{ __('zimam.monitoring.live.none') }}</p>
            @else
                <p class="zimam-note">
                    {{ $stale
                        ? __('zimam.monitoring.live.stale', ['time' => Warnings::time($reading->at)])
                        : __('zimam.monitoring.live.as_of', ['time' => Warnings::time($reading->at)]) }}
                </p>

                <dl class="zimam-facts">
                    @foreach ($rows as [$label, $value])
                        <dt>{{ $label }}</dt>
                        <dd>{{ $value }}</dd>
                    @endforeach
                    <dt>{{ __('zimam.monitoring.live.disks') }}</dt>
                    <dd>
                        @foreach ($disks as $disk)
                            <div wire:key="disk-{{ $loop->index }}">
                                <strong>{{ $disk['mount'] }}</strong> {{ $disk['usage'] }}@if ($disk['inodes']), {{ $disk['inodes'] }}@endif
                            </div>
                        @endforeach
                    </dd>
                </dl>
            @endif
        </section>

        @if ($reading)
            <section aria-labelledby="zimam-live-warnings">
                <div class="zimam-section-head">
                    <h2 id="zimam-live-warnings">{{ __('zimam.monitoring.live.warnings') }}</h2>
                </div>
                @if ($warnings === [])
                    <p class="zimam-note">{{ __('zimam.monitoring.live.no_warnings') }}</p>
                @else
                    @include('filament.widgets.warning-list', ['warnings' => $warnings])
                @endif
            </section>
        @endif
    </div>
</x-filament-widgets::widget>
