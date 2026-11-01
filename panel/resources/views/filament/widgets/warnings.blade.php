@php
    use App\Filament\Widgets\Warnings;
    use Filament\Support\Icons\Heroicon;
@endphp

<x-filament-widgets::widget class="zimam-bulletin">
    <div wire:poll.10s>
        <x-filament::section>
            <div aria-live="polite">
                <h2 class="fi-section-header-heading">{{ $headline }}</h2>
                <p>{{ $lede }}</p>
                <p class="fi-section-header-description">
                    @if ($status?->fresh)
                        {{ __('zimam.dashboard.issued', ['issued' => Warnings::time($status->issuedAt), 'next' => Warnings::time($status->nextAt())]) }}
                    @elseif ($status)
                        <s>{{ __('zimam.dashboard.issued_at', ['issued' => Warnings::time($status->issuedAt)]) }}</s> · {{ __('zimam.dashboard.waiting') }}
                    @else
                        {{ __('zimam.dashboard.waiting') }}
                    @endif
                </p>
            </div>
        </x-filament::section>

        @if ($warnings !== [])
            <ul aria-label="{{ __('zimam.dashboard.warnings_label') }}">
                @foreach ($warnings as $warning)
                    <li wire:key="warning-{{ $loop->index }}">
                        <x-filament::callout
                            :color="$warning['severity'] === 'critical' ? 'danger' : 'warning'"
                            :icon="$warning['severity'] === 'critical' ? Heroicon::OutlinedExclamationCircle : Heroicon::OutlinedExclamationTriangle"
                            :heading="$warning['title']"
                            :description="new \Illuminate\Support\HtmlString(
                                '<strong>'.e(__('zimam.dashboard.severity.'.$warning['severity'])).'</strong> · '.e($warning['at'])
                                .'<br>'.e($warning['detail'])
                            )"
                        />
                    </li>
                @endforeach
            </ul>
        @endif
    </div>
</x-filament-widgets::widget>
