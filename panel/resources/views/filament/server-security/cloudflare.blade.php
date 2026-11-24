{{-- The Server security page's Cloudflare mode (R06-D7, D9). --}}
<x-filament::section id="cloudflare" :heading="__('zimam.server_security.cloudflare.heading')" :description="__('zimam.server_security.cloudflare.lede')">
    <form wire:submit.prevent class="grid gap-6">
        {{ $this->form }}

        @if (($data['enabled'] ?? false) && ($ranges = $this->ranges()) !== null)
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ __('zimam.server_security.cloudflare.fresh', ['detail' => $ranges]) }}</p>
        @endif

        <div>{{ $this->saveAction }}</div>
    </form>

    <x-filament-actions::modals />
</x-filament::section>
