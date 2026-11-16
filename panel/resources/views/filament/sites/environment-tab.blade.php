{{-- A site's Environment tab (R02-D10). It never polls: the .env is in no request but the one that reads it and the one that saves it. --}}
<div class="zimam-stack">
    <x-filament::section :heading="__('zimam.environment.heading')" :description="__('zimam.environment.help')">
        @if ($revealed)
            <form wire:key="env-editor-{{ $editorKey }}" x-data x-on:submit.prevent="$wire.save($refs.content.value)" class="grid gap-3 text-sm">
                @if ($missing)
                    <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.environment.missing') }}</p>
                @endif
                {{--
                    The content is rendered only in the response that read it. wire:ignore keeps the browser's
                    copy, and what is typed into it, through later requests, whose HTML has no content; it
                    goes back only as save()'s argument. No name and no wire:model, so neither a form post
                    nor the component's state ever carries it. The newline after the tag keeps a leading
                    blank line: the parser drops the first one.
                --}}
                <label wire:ignore class="grid gap-1">
                    <span class="font-medium">{{ __('zimam.environment.field') }}</span>
                    <textarea x-ref="content" rows="20" spellcheck="false" autocomplete="off" autocapitalize="off" class="block w-full rounded-lg border border-gray-300 bg-white p-3 font-mono text-xs dark:border-white/10 dark:bg-white/5">
{{ $content ?? '' }}</textarea>
                </label>
                <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.environment.limit', ['max' => \App\Agent\Commands\SiteEnvWrite::MAX_BYTES]) }} {{ __('zimam.environment.line_endings') }}</p>
                @error('content')
                    <p class="text-danger-600 dark:text-danger-400">{{ $message }}</p>
                @enderror
                <div class="flex flex-wrap gap-3">
                    <x-filament::button type="submit" :disabled="$site->hasUnfinishedJob()">
                        {{ __('zimam.environment.save') }}
                    </x-filament::button>
                    <x-filament::button color="gray" wire:click="cancel">
                        {{ __('zimam.environment.cancel') }}
                    </x-filament::button>
                </div>
            </form>
        @else
            <div class="grid gap-3 text-sm">
                <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.environment.hidden') }}</p>
                @if ($readError !== null)
                    <p class="text-danger-600 dark:text-danger-400">{{ $readError }}</p>
                @endif
                <div>{{ $this->revealAction }}</div>
            </div>
        @endif
    </x-filament::section>

    <x-filament-actions::modals />
</div>
