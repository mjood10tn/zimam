@php
    use App\Filament\Widgets\Warnings;

    $stale = $status && ! $status->fresh;
@endphp

<x-filament-widgets::widget class="zimam-state">
    <section wire:poll.10s @class(['zimam-server', 'zimam-stale' => $stale]) aria-labelledby="zimam-server-heading">
        <div class="zimam-section-head">
            <h2 id="zimam-server-heading">{{ __('zimam.server.heading') }}</h2>
            @if ($stale)
                {{-- A stale value keeps its time, struck through, in the age column (DESIGN.md). --}}
                <span class="zimam-age"><s>{{ Warnings::time($status->issuedAt) }}</s></span>
            @endif
        </div>

        @if ($stale)
            <p class="zimam-note">{{ __('zimam.server.as_of', ['time' => Warnings::time($status->issuedAt)]) }}</p>
        @endif

        @if ($rows === [])
            <p class="zimam-note">{{ __('zimam.dashboard.lede.no_report') }}</p>
        @else
            <dl class="zimam-facts">
                @foreach ($rows as [$label, $value])
                    <dt>{{ $label }}</dt>
                    <dd>{{ $value }}</dd>
                @endforeach
            </dl>
        @endif
    </section>
</x-filament-widgets::widget>
