{{-- The Server security page's SSH settings (R06-D4). --}}
<x-filament::section :heading="__('zimam.server_security.ssh.heading')" :description="__('zimam.server_security.ssh.lede')">
    <form wire:submit.prevent class="grid gap-6">
        {{ $this->form }}

        <div>{{ $this->saveAction }}</div>
    </form>

    <x-filament-actions::modals />
</x-filament::section>
