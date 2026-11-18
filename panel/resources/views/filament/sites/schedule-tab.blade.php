{{-- A site's Schedule tab (v0.4.0): the Laravel scheduler's switch, the cron jobs with their next runs, and how they run. --}}
<div class="zimam-stack">
    <x-filament::section :heading="__('zimam.schedule.scheduler.heading')" :description="$site->runsPhp() ? __('zimam.schedule.scheduler.lede') : __('zimam.schedule.scheduler.static')">
        @if ($site->runsPhp())
            <x-slot name="afterHeader">
                <div class="flex flex-wrap items-center gap-2">
                    <x-filament::badge :color="$site->scheduler_enabled ? 'success' : 'gray'">
                        {{ $site->scheduler_enabled ? __('zimam.schedule.scheduler.on') : __('zimam.schedule.scheduler.off') }}
                    </x-filament::badge>
                    {{ $this->schedulerAction }}
                </div>
            </x-slot>
        @endif
    </x-filament::section>

    <x-filament::section :heading="__('zimam.schedule.heading')" :description="__('zimam.schedule.lede')">
        <x-slot name="afterHeader">
            {{ $this->addAction }}
        </x-slot>

        <p class="mb-3 text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.schedule.zone', ['zone' => $zone]) }}</p>

        {{ $this->table }}
    </x-filament::section>

    <x-filament::section :heading="__('zimam.schedule.help.heading')" collapsible>
        <ul class="grid gap-2 text-sm">
            <li>{{ __('zimam.schedule.help.zone', ['zone' => $zone]) }}</li>
            <li>{{ __('zimam.schedule.help.days') }}</li>
            <li>{{ __('zimam.schedule.help.overlap') }}</li>
            <li>{{ __('zimam.schedule.help.output') }}</li>
            <li>{{ __('zimam.schedule.help.percent') }}</li>
        </ul>
    </x-filament::section>

    <x-filament-actions::modals />
</div>
