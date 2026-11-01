@php
    use App\Filament\Widgets\Warnings;
    use Filament\Support\Icons\Heroicon;
@endphp

{{-- Shown on every panel page while the agent doesn't answer (D10, §12). --}}
<div role="alert">
    <x-filament::callout
        color="danger"
        :icon="Heroicon::OutlinedExclamationCircle"
        :heading="__('zimam.agent.banner.heading')"
        :description="($since ? __('zimam.agent.banner.since', ['time' => Warnings::time($since)]).' ' : '').__('zimam.agent.banner.body')"
    />
</div>
