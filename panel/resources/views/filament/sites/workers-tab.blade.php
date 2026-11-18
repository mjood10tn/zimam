{{-- A site's Workers tab (v0.4.0): the list, each process's state, and the notes on what workers need. --}}
<div class="zimam-stack">
    @if (! $site->runsPhp())
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.workers.static') }}</p>
    @else
        @if ($memoryWarning)
            @include('filament.widgets.warning-list', ['warnings' => [[
                'severity' => 'warning',
                'title' => __('zimam.workers.memory_over_title'),
                'detail' => $memoryWarning,
                'at' => '',
            ]]])
        @endif

        <x-filament::section :heading="__('zimam.workers.heading')" :description="__('zimam.workers.lede')">
            <x-slot name="afterHeader">
                <div class="flex flex-wrap gap-2">
                    {{ $this->restartAllAction }}
                    {{ $this->addAction }}
                </div>
            </x-slot>

            @if ($statusError)
                <p class="mb-3 text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.workers.status_unread', ['reason' => $statusError]) }}</p>
            @endif

            {{ $this->table }}
        </x-filament::section>

        <x-filament::section :heading="__('zimam.workers.help.heading')" collapsible>
            <ul class="grid gap-2 text-sm">
                <li>{{ __('zimam.workers.help.restarts') }}</li>
                <li>{{ __('zimam.workers.help.timeout') }}</li>
                <li>{{ __('zimam.workers.help.config_cache') }}</li>
                <li>{{ __('zimam.workers.help.horizon', ['prefix' => $horizonPrefix]) }}</li>
                <li>{{ __('zimam.workers.help.reverb') }}</li>
            </ul>
        </x-filament::section>
    @endif

    <x-filament-actions::modals />
</div>
