@php
    use App\Filament\Widgets\Warnings;
    use Filament\Support\Icons\Heroicon;
@endphp

{{-- The triage column (dashboard brief §6): the bulletin opening, then the warnings in force. --}}
<x-filament-widgets::widget class="zimam-triage">
    <div wire:poll.10s class="zimam-stack">
        <section class="zimam-bulletin">
            {{-- Only the headline and the sentence are live: the issued line changes on every 10 s poll. --}}
            <div aria-live="polite">
                <h1 class="zimam-headline">{{ $headline }}</h1>
                <p class="zimam-lede">{{ $lede }}</p>
            </div>
            <p class="zimam-issued">
                @if ($status?->fresh)
                    {{ __('zimam.dashboard.issued', ['issued' => Warnings::time($status->issuedAt), 'next' => Warnings::time($status->nextAt())]) }}
                @elseif ($status)
                    <s>{{ __('zimam.dashboard.issued_at', ['issued' => Warnings::time($status->issuedAt)]) }}</s> · {{ __('zimam.dashboard.waiting') }}
                @else
                    {{ __('zimam.dashboard.waiting') }}
                @endif
            </p>
        </section>

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
    </div>
</x-filament-widgets::widget>
