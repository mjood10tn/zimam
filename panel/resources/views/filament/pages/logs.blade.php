<x-filament-panels::page>
    <div class="zimam-stack">
        <label class="grid gap-1 text-sm">
            <span>{{ __('zimam.logs.source') }}</span>
            <select wire:model.live="source" class="rounded-lg border px-2 py-1">
                @foreach ($sources as $id => $label)
                    <option value="{{ $id }}">{{ $label }}</option>
                @endforeach
            </select>
        </label>
        <livewire:log-viewer :source="$source" :key="'log-'.$source" />
    </div>
</x-filament-panels::page>
