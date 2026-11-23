{{-- The new destination's keys in the add dialog, from its sealed state; gone once the destination is sent. --}}
@if ($keys !== null)
    <div class="grid gap-3 text-sm">
        @include('filament.backups.key-fields', ['keys' => $keys, 'id' => 'new'])
        <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.backups.destinations.add.once') }}</p>
    </div>
@endif
