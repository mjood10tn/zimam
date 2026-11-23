{{-- Backup runs, on the Backups page and a site's Backups tab. --}}
<x-filament::section :heading="__('zimam.backups.runs.heading')" :description="$site === null ? __('zimam.backups.runs.lede') : null">
    {{ $this->table }}

    <x-filament-actions::modals />
</x-filament::section>
