@php
    use App\Filament\Widgets\Warnings;
@endphp

<x-filament-panels::page>
    <div class="zimam-stack">
        @forelse ($sites as $site)
            <section class="zimam-server" wire:key="site-{{ $site['name'] }}" aria-labelledby="zimam-site-{{ $site['name'] }}">
                <div class="zimam-section-head">
                    <h2 id="zimam-site-{{ $site['name'] }}">{{ $site['name'] }}</h2>
                    @if ($site['checked'])
                        <span class="zimam-age">{{ __('zimam.dashboard.checked', ['time' => Warnings::time($site['checked'], 'j M H:i')]) }}</span>
                    @endif
                </div>
                <dl class="zimam-facts">
                    <dt>{{ __('zimam.checks.https.label') }}</dt>
                    <dd>{{ __('zimam.checks.https.'.$site['https']) }}</dd>
                    <dt>{{ __('zimam.checks.certificate') }}</dt>
                    <dd @class(['zimam-critical' => $site['expires_soon']])>
                        {{ $site['expires'] ? __('zimam.checks.expires', ['date' => Warnings::time($site['expires'], 'j M Y')]) : __('zimam.sites.certificate.none') }}
                    </dd>
                    <dt>{{ __('zimam.checks.protections.label') }}</dt>
                    <dd>{{ $site['protections'] === [] ? __('zimam.checks.protections.none') : implode(', ', array_map(fn (string $p): string => __('zimam.checks.protections.'.$p), $site['protections'])) }}</dd>
                    <dt>{{ __('zimam.checks.failing') }}</dt>
                    <dd>
                        @if ($site['checked'] === null)
                            {{ __('zimam.checks.not_run') }}
                        @elseif ($site['failing'] === [])
                            {{ __('zimam.checks.all_pass') }}
                        @else
                            <ul>
                                @foreach ($site['failing'] as $finding)
                                    <li wire:key="finding-{{ $finding->id }}">
                                        <strong>{{ __('zimam.checks.names.'.$finding->check) }}</strong>
                                        ({{ __('zimam.checks.status.'.$finding->status) }}): {{ $finding->detail }}
                                    </li>
                                @endforeach
                            </ul>
                        @endif
                    </dd>
                </dl>
            </section>
        @empty
            <p class="zimam-note">{{ __('zimam.sites.empty') }}</p>
        @endforelse

        <section class="zimam-server" aria-labelledby="zimam-server-checks">
            <div class="zimam-section-head">
                <h2 id="zimam-server-checks">{{ __('zimam.checks.server') }}</h2>
            </div>
            @if ($server === [])
                <p class="zimam-note">{{ __('zimam.checks.not_run') }}</p>
            @else
                <dl class="zimam-facts">
                    @foreach ($server as $finding)
                        <dt wire:key="server-{{ $finding->check }}">{{ __('zimam.checks.names.'.$finding->check) }}</dt>
                        <dd>
                            <x-filament::badge :color="match ($finding->status) { 'pass' => 'success', 'fail' => 'danger', default => 'warning' }">
                                {{ __('zimam.checks.status.'.$finding->status) }}
                            </x-filament::badge>
                            {{ $finding->detail }}
                        </dd>
                    @endforeach
                </dl>
            @endif
        </section>
    </div>
</x-filament-panels::page>
