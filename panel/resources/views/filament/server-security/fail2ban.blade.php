{{-- The Server security page's fail2ban (R06-D5). --}}
<x-filament::section
    id="fail2ban"
    :heading="__('zimam.server_security.fail2ban.heading')"
    :description="match (true) {
        $status === null => __('zimam.server_security.fail2ban.unknown'),
        ! $status['installed'] => __('zimam.server_security.fail2ban.not_installed'),
        ! $status['running'] => __('zimam.server_security.fail2ban.stopped'),
        default => __('zimam.server_security.fail2ban.running'),
    }"
>
    <div class="grid gap-6">
        @if ($this->cloudflareNote())
            <p class="text-sm">{{ __('zimam.server_security.fail2ban.cloudflare_note') }}</p>
        @endif

        @if ($status !== null && $status['running'])
            <div>
                <h3 class="text-sm font-semibold">{{ __('zimam.server_security.fail2ban.banned.heading') }}</h3>
                @php($bans = collect($status['jails'])->flatMap(fn (array $j): array => array_map(fn (array $b): array => $b + ['jail' => $j['name']], $j['banned'])))
                @if ($bans->isEmpty())
                    <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">{{ __('zimam.server_security.fail2ban.banned.none') }}</p>
                @else
                    <ul class="mt-1 text-sm tabular-nums">
                        @foreach ($bans as $ban)
                            <li wire:key="ban-{{ $ban['jail'] }}-{{ $ban['address'] }}" class="flex flex-wrap items-center justify-between gap-2 border-b border-gray-200 py-2 last:border-b-0 dark:border-white/10">
                                <span>
                                    {{ $ban['address'] }} · {{ __('zimam.server_security.fail2ban.jails.'.$ban['jail']) }} ·
                                    {{ __('zimam.server_security.fail2ban.banned.until', ['time' => \App\Support\DisplayTime::format(\Carbon\CarbonImmutable::parse($ban['until']), 'j M H:i')]) }}
                                </span>
                                {{ ($this->unbanAction)(['jail' => $ban['jail'], 'address' => $ban['address']]) }}
                            </li>
                        @endforeach
                    </ul>
                @endif
            </div>
        @endif

        <form wire:submit.prevent class="grid gap-6">
            {{ $this->form }}

            <div>{{ $this->saveAction }}</div>
        </form>
    </div>

    <x-filament-actions::modals />
</x-filament::section>
