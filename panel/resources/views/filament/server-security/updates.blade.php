{{-- Server security → Updates (R06-D12..D16). --}}
@php
    $status = $this->status();
    $updates = $status['updates'] ?? [];
    $installing = $this->installing();
    $scheduled = $this->scheduledReboot();
@endphp
<x-filament::section
    id="updates"
    :heading="__('zimam.server_security.updates.heading')"
    :description="is_int($updates['total'] ?? null)
        ? trans_choice('zimam.server_security.updates.counts', $updates['total'], ['count' => $updates['total'], 'security' => $updates['security']])
        : __('zimam.server_security.updates.counts_unknown')"
>
    <div class="grid gap-6" @if ($installing !== null) wire:poll.5s @endif>
        @if (is_string($updates['checked_at'] ?? null))
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ __('zimam.server_security.updates.checked', ['time' => \App\Support\DisplayTime::format(\Carbon\CarbonImmutable::parse($updates['checked_at']), 'j M H:i')]) }}</p>
        @endif

        <div class="flex flex-wrap gap-3">{{ $this->installAction }}</div>

        @if ($installing !== null)
            <livewire:job-log :agent-job="$installing" :key="'os-update-log-'.$installing->id" />
        @endif

        <form wire:submit.prevent class="grid gap-4">
            @if (! isset($status['auto_updates']))
                <p class="text-sm">{{ __('zimam.server_security.updates.auto.unknown') }}</p>
            @endif
            {{ $this->form }}
            <div>{{ $this->saveAutoAction }}</div>
        </form>

        <div>
            <p class="text-sm">
                @if ($scheduled !== null)
                    {{ __('zimam.server_security.updates.reboot.scheduled', ['time' => \App\Support\DisplayTime::format($scheduled, 'j M H:i')]) }}
                @elseif ($status['reboot_required'] ?? false)
                    {{ __('zimam.server_security.updates.reboot.required') }}
                @else
                    {{ __('zimam.server_security.updates.reboot.not_required') }}
                @endif
            </p>
            <div class="mt-3 flex flex-wrap gap-3">
                {{ $this->scheduleRebootAction }}
                {{ $this->cancelRebootAction }}
                {{ $this->rebootNowAction }}
            </div>
        </div>

        <div>
            <h3 class="text-sm font-semibold">{{ __('zimam.server_security.updates.runs.heading') }}</h3>
            @if ($runs->isEmpty())
                <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">{{ __('zimam.server_security.updates.runs.none') }}</p>
            @else
                <ul class="mt-1 text-sm tabular-nums">
                    @foreach ($runs as $run)
                        @php($scope = __('zimam.server_security.updates.install.scopes.'.$run->scope))
                        <li wire:key="os-update-run-{{ $run->id }}" class="border-b border-gray-200 py-2 last:border-b-0 dark:border-white/10">
                            <div class="flex flex-wrap justify-between gap-2">
                                <span class="font-semibold">
                                    @if ($run->status === \App\Models\OsUpdateRun::FAILED)
                                        {{ __('zimam.server_security.updates.runs.failed', ['scope' => $scope]) }}
                                    @elseif ($run->status === \App\Models\OsUpdateRun::RUNNING)
                                        {{ __('zimam.server_security.updates.install.running') }}
                                    @else
                                        {{ trans_choice('zimam.server_security.updates.runs.summary', count($run->upgraded ?? []), ['scope' => $scope]) }}
                                    @endif
                                </span>
                                <span>{{ \App\Support\DisplayTime::format($run->started_at, 'j M H:i') }}</span>
                            </div>
                            @if ($run->upgraded)
                                <p class="mt-1">
                                    {{ implode(', ', array_map(fn (array $p): string => $p['from'] === ''
                                        ? __('zimam.server_security.updates.runs.new', ['package' => $p['package'], 'to' => $p['to']])
                                        : __('zimam.server_security.updates.runs.upgraded', $p), $run->upgraded)) }}
                                </p>
                            @endif
                            @if ($run->restarted)
                                <p class="mt-1">{{ __('zimam.server_security.updates.runs.restarted', ['units' => implode(', ', $run->restarted)]) }}</p>
                            @endif
                            @if ($run->reboot_required)
                                <p class="mt-1">{{ __('zimam.server_security.updates.runs.reboot') }}</p>
                            @endif
                        </li>
                    @endforeach
                </ul>
            @endif
        </div>
    </div>

    <x-filament-actions::modals />
</x-filament::section>
