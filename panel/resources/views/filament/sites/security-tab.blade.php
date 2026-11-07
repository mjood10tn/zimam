{{-- A site's Security tab (§9.6): every setting, saved as one job. --}}
<div>
    <form wire:submit="save" class="zimam-stack">
        {{ $this->form }}

        <div>
            <x-filament::button type="submit" :disabled="$site->hasUnfinishedJob()">
                {{ __('zimam.security.save') }}
            </x-filament::button>
        </div>
    </form>

    {{-- The repeater's actions (add, delete, reorder) are hosted by the tab. --}}
    <x-filament-actions::modals />
</div>
