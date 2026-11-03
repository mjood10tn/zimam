<x-filament-panels::page>
    <form wire:submit="save" class="zimam-stack">
        {{ $this->form }}
        <div>
            <x-filament::button type="submit">{{ __('zimam.settings.save') }}</x-filament::button>
        </div>
    </form>
</x-filament-panels::page>
