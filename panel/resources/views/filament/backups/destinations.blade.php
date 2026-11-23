{{-- The Backups page's destinations. Keys read back for "Show keys again" are in no request but the one that read them. --}}
<x-filament::section :heading="__('zimam.backups.destinations.heading')" :description="__('zimam.backups.destinations.lede')">
    <x-slot name="afterHeader">{{ $this->addAction }}</x-slot>

    <div class="grid gap-4">
        @if ($revealKey !== null)
            <div wire:key="keys-{{ $revealKey }}" class="grid gap-3 text-sm">
                {{--
                    The keys are rendered only in the response that read them. wire:ignore keeps the browser's
                    copy through later requests, whose HTML has none. No name and no wire:model, so neither a
                    form post nor the component's state carries them.
                --}}
                <div wire:ignore class="grid gap-3">
                    @if ($revealed !== null)
                        @include('filament.backups.key-fields', ['keys' => ['password' => $revealed['encryption_password'], 'salt' => $revealed['encryption_salt']], 'id' => $revealKey])
                    @endif
                </div>
                <div>
                    <x-filament::button color="gray" wire:click="closeKeys">{{ __('zimam.backups.destinations.keys.done') }}</x-filament::button>
                </div>
            </div>
        @endif

        {{ $this->table }}
    </div>

    <x-filament-actions::modals />
</x-filament::section>
