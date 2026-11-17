{{-- A site's Cache tab: its Valkey user and the limits of sharing one Valkey. The new password is in no request but the one that made it, and the one that writes it to .env. --}}
<div class="zimam-stack">
    {{-- An open password block stays whatever the cached status says: replacing it would lose the password. --}}
    @if (! $installed && $rotationKey === null)
        <x-filament::section :heading="__('zimam.cache.tab.heading')">
            <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.cache.tab.not_installed') }}</p>
            <p class="mt-3 text-sm">
                <a href="{{ \App\Filament\Pages\Cache::getUrl() }}" class="underline">{{ __('zimam.cache.tab.install_link') }}</a>
            </p>
        </x-filament::section>
    @else
        <x-filament::section :heading="__('zimam.cache.tab.user')">
            <div class="grid gap-3 text-sm">
                <p>
                    <strong>{{ \App\Models\SiteCredential::usernameFor($site->name) }}</strong>
                    ·
                    @if ($credential?->rotated_at)
                        {{ __('zimam.cache.tab.rotated', ['time' => \App\Support\DisplayTime::format($credential->rotated_at, 'j M Y, H:i')]) }}
                    @else
                        {{ __('zimam.cache.tab.user_never') }}
                    @endif
                </p>
                <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.cache.tab.host') }}</p>
                <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.cache.tab.env_note') }}</p>

                @include('filament.sites.rotation', ['lang' => 'cache'])
            </div>
        </x-filament::section>

        <x-filament::section :heading="__('zimam.cache.tab.limits_heading')">
            <ul class="grid list-disc gap-2 pl-5 text-sm">
                @foreach (['user', 'flush', 'scan', 'rotate'] as $limit)
                    <li>{{ __("zimam.cache.tab.limit_{$limit}", ['prefix' => \App\Models\SiteCredential::usernameFor($site->name).':']) }}</li>
                @endforeach
            </ul>
        </x-filament::section>
    @endif

    <x-filament-actions::modals />
</div>
