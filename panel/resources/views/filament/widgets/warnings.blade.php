@php
    use App\Filament\Widgets\Warnings;
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

        @include('filament.widgets.warning-list', ['warnings' => $warnings])
    </div>
</x-filament-widgets::widget>
