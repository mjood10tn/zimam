{{-- The Server security page's firewall rules (R06-D3). --}}
<x-filament::section
    id="firewall"
    :heading="__('zimam.server_security.firewall.heading')"
    :description="match ($active) {
        true => __('zimam.server_security.firewall.active'),
        false => __('zimam.server_security.firewall.inactive'),
        null => __('zimam.server_security.firewall.stale'),
    }"
>
    <x-slot name="afterHeader">{{ $this->addAction }}</x-slot>

    {{ $this->table }}

    <div class="mt-6">
        <h3 class="text-sm font-semibold">{{ __('zimam.server_security.firewall.external.heading') }}</h3>
        @if ($external === [])
            <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">{{ __('zimam.server_security.firewall.external.none') }}</p>
        @else
            <ul class="mt-1 text-sm tabular-nums">
                @foreach ($external as $rule)
                    <li class="border-b border-gray-200 py-2 last:border-b-0 dark:border-white/10">
                        {{ __('zimam.server_security.firewall.external.row', ['action' => $rule['action'], 'proto' => $rule['proto'], 'port' => $rule['port'], 'from' => $rule['from']]) }}
                        @if ($rule['ipv6'])
                            {{ __('zimam.server_security.firewall.external.v6') }}
                        @endif
                    </li>
                @endforeach
            </ul>
        @endif
    </div>

    <x-filament-actions::modals />
</x-filament::section>
