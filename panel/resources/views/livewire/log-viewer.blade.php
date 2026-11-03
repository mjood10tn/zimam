{{-- One log source (§11.2). Following polls every 3 seconds. --}}
<div @if ($live) wire:poll.3s="poll" @endif class="zimam-stack">
    <div class="flex flex-wrap items-end gap-4">
        <label class="grid gap-1 text-sm">
            <span>{{ __('zimam.logs.search') }}</span>
            <input type="search" maxlength="200" wire:model.live.debounce.500ms="search" class="fi-input rounded-lg border px-2 py-1" />
        </label>
        @if ($this->isJournal())
            <label class="grid gap-1 text-sm">
                <span>{{ __('zimam.logs.priority') }}</span>
                <select wire:model.live="priority" class="rounded-lg border px-2 py-1">
                    <option value="">{{ __('zimam.logs.priorities.all') }}</option>
                    @foreach (range(0, 7) as $p)
                        <option value="{{ $p }}">{{ __('zimam.logs.priorities.'.$p) }}</option>
                    @endforeach
                </select>
            </label>
        @endif
        @if ($archives !== [])
            <label class="grid gap-1 text-sm">
                <span>{{ __('zimam.logs.archive') }}</span>
                <select wire:model.live="archive" class="rounded-lg border px-2 py-1">
                    <option value="">{{ __('zimam.logs.current') }}</option>
                    @foreach ($archives as $a)
                        <option value="{{ $a['name'] }}">{{ $a['name'] }}</option>
                    @endforeach
                </select>
            </label>
        @endif
        @if ($archive === null && $search === '')
            <label class="flex items-center gap-2 text-sm">
                <input type="checkbox" wire:model.live="follow" />
                <span>{{ __('zimam.logs.follow') }}</span>
            </label>
        @endif
    </div>

    @if ($error)
        <p class="zimam-note" role="alert">{{ $error }}</p>
    @endif

    @if ($back !== null && $search === '')
        <div><x-filament::button color="gray" size="sm" wire:click="loadOlder">{{ __('zimam.logs.older') }}</x-filament::button></div>
    @endif

    @if ($lines === [])
        <p class="zimam-note">{{ __('zimam.logs.empty') }}</p>
    @else
        <pre class="overflow-x-auto text-xs leading-5"><code>@foreach ($lines as $line)<span wire:key="{{ $line['key'] }}">@if ($line['meta'] !== ''){{ $line['meta'] }} @endif{{ $line['text'] }}
</span>@endforeach</code></pre>
    @endif

    @if ($stopped !== '')
        <p class="zimam-note">{{ __('zimam.logs.search_stopped.'.$stopped) }}</p>
    @endif
</div>
