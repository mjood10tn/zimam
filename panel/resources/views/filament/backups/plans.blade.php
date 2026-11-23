{{-- The Backups page's plans. --}}
<x-filament::section :heading="__('zimam.backups.plans.heading')" :description="__('zimam.backups.plans.lede')">
    <x-slot name="afterHeader">{{ $this->createAction }}</x-slot>

    {{ $this->table }}

    <x-filament-actions::modals />
</x-filament::section>
