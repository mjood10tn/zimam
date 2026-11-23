{{-- A site's Backups tab (v0.5.0): the plans that include it, their runs with restore, and its recent restores. --}}
<div class="zimam-stack" @if ($pending) wire:poll.5s @endif>
    <x-filament::section :heading="__('zimam.backups.tab.plans')">
        <x-slot name="afterHeader">
            <a href="{{ \App\Filament\Pages\Backups::getUrl() }}" class="text-sm underline">{{ __('zimam.backups.tab.manage') }}</a>
        </x-slot>
        @if ($plans->isEmpty())
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.backups.tab.no_plans') }}</p>
        @else
            <ul class="grid gap-2 text-sm">
                @foreach ($plans as $plan)
                    <li wire:key="plan-{{ $plan->id }}">
                        <strong>{{ $plan->key }}</strong> · {{ $plan->scheduleLabel() }} · {{ __('zimam.backups.plans.to', ['destination' => $plan->destination->key]) }}
                    </li>
                @endforeach
            </ul>
        @endif
    </x-filament::section>

    @if ($plans->isNotEmpty())
        @livewire(\App\Filament\Pages\Backups\RunsTable::class, ['site' => $site], key('site-backup-runs-'.$site->id))
    @endif

    <x-filament::section :heading="__('zimam.backups.tab.restores')">
        @if ($restores->isEmpty())
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.backups.tab.no_restores') }}</p>
        @else
            <ul class="grid gap-3 text-sm">
                @foreach ($restores as $job)
                    <li wire:key="restore-{{ $job->id }}" class="grid gap-1">
                        <p>
                            <strong>{{ $job->params['part'] ?? '' }}</strong>
                            · {{ __('zimam.backups.tab.from', ['plan' => $job->params['plan'] ?? '', 'backup' => $job->params['backup'] ?? '']) }}
                            · {{ __("zimam.jobs.status.{$job->status}") }}
                        </p>
                        @if (isset($job->result['folder']))
                            <p>{{ __('zimam.backups.tab.into_folder', ['folder' => $job->result['folder']]) }}</p>
                        @elseif (isset($job->result['database']))
                            <p>{{ __('zimam.backups.tab.into_database', ['database' => $job->result['database']]) }}</p>
                        @endif
                        @if (! empty($job->result['skipped_links']))
                            <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.backups.tab.skipped_links') }}</p>
                            <ul class="list-disc pl-5">
                                @foreach ($job->result['skipped_links'] as $link)
                                    <li>{{ $link }}</li>
                                @endforeach
                            </ul>
                        @endif
                        @if ($job->status === \App\Models\AgentJob::FAILED && $job->error_message)
                            <p class="text-danger-600 dark:text-danger-400">{{ $job->error_message }}</p>
                        @endif
                    </li>
                @endforeach
            </ul>
        @endif
    </x-filament::section>
</div>
