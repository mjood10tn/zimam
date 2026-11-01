@php
    use App\Filament\Widgets\Warnings;
@endphp

<x-filament-widgets::widget>
    <x-filament::section
        :heading="__('zimam.server.heading')"
        :description="$status && ! $status->fresh ? __('zimam.server.as_of', ['time' => Warnings::time($status->issuedAt)]) : null"
    >
        @if ($rows === [])
            <p>{{ __('zimam.dashboard.lede.no_report') }}</p>
        @else
            <dl>
                @foreach ($rows as [$label, $value])
                    <div>
                        <dt class="fi-section-header-description">{{ $label }}</dt>
                        <dd>{{ $value }}</dd>
                    </div>
                @endforeach
            </dl>
        @endif
    </x-filament::section>
</x-filament-widgets::widget>
