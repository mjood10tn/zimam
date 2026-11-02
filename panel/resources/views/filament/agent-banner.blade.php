@php
    use App\Filament\Widgets\Warnings;
    use Filament\Support\Icons\Heroicon;

    // The command sits in the sentence as :command, set in code.
    [$before, $after] = explode(':command', __('zimam.agent.banner.body'), 2) + [1 => ''];
@endphp

{{-- Shown on every panel page while the agent doesn't answer (D10, §12). --}}
<div role="alert" class="zimam-banner">
    <x-filament::icon :icon="Heroicon::OutlinedExclamationCircle" class="zimam-icon" />
    <strong>{{ __('zimam.agent.banner.heading') }}</strong>
    <p>
        @if ($since)
            {{ __('zimam.agent.banner.since', ['time' => Warnings::time($since)]) }}
        @endif
        {{ $before }}<code>{{ __('zimam.agent.banner.command') }}</code>{{ $after }}
    </p>
</div>
