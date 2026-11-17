{{-- A site's Database tab: its databases and MySQL user. The new password is in no request but the one that made it, and the one that writes it to .env. --}}
<div class="zimam-stack">
    <x-filament::section :heading="__('zimam.databases.tab.heading')" :description="__('zimam.databases.tab.help')">
        @if (! $installed)
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.databases.not_installed') }}</p>
        @elseif ($databases->isEmpty())
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.databases.tab.none') }}</p>
        @else
            <dl class="zimam-facts">
                @foreach ($databases as $database)
                    <dt wire:key="database-{{ $database->id }}">{{ $database->name }}</dt>
                    <dd>
                        {{ \App\Support\Bytes::format($database->size_bytes) }}
                        @if ($database->status !== \App\Models\Database::ACTIVE)
                            · {{ __("zimam.databases.status.{$database->status}") }}
                        @endif
                    </dd>
                @endforeach
            </dl>
            <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.databases.sizes_note') }}</p>
        @endif
        <p class="mt-3 text-sm">
            <a href="{{ \App\Filament\Pages\Databases::getUrl() }}" class="underline">{{ __('zimam.databases.tab.manage') }}</a>
        </p>
    </x-filament::section>

    <x-filament::section :heading="__('zimam.databases.tab.user')">
        <div class="grid gap-3 text-sm">
            <p>
                <strong>{{ \App\Models\SiteCredential::usernameFor($site->name) }}</strong>
                ·
                @if ($credential?->rotated_at)
                    {{ __('zimam.databases.tab.rotated', ['time' => \App\Support\DisplayTime::format($credential->rotated_at, 'j M Y, H:i')]) }}
                @else
                    {{ __('zimam.databases.tab.user_never') }}
                @endif
            </p>
            <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.databases.tab.host') }}</p>

            @include('filament.sites.rotation', ['lang' => 'databases', 'envFields' => 'filament.sites.database-env-fields'])
        </div>
    </x-filament::section>

    <x-filament-actions::modals />
</div>
