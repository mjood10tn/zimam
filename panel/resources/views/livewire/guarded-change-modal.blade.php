{{-- The guarded-change countdown (R06-D1, D2). It polls only while a change is being applied or waits. --}}
<div @if ($this->polls()) wire:poll.2s="settle" @endif>
    <x-filament-actions::modals />
</div>
