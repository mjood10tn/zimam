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

            @if ($rotationFailure !== null)
                <p class="text-danger-600 dark:text-danger-400">{{ $rotationFailure }}</p>
            @endif

            @if ($rotationKey !== null)
                {{-- Polls only while the job runs; the poll's responses carry no password. --}}
                <div wire:key="rotation-{{ $rotationKey }}" @if ($pending) wire:poll.3s="refreshRotation" @endif class="grid gap-3">
                    <p>{{ $rotationDone ? __('zimam.databases.rotate.ready') : __('zimam.databases.rotate.pending') }}</p>
                    {{--
                        The password is rendered only in the response that made it. wire:ignore keeps the
                        browser's copy through later requests, whose HTML has none; it is hidden until the job
                        has succeeded, and goes back to the server only as writeEnv()'s argument. No name and no
                        wire:model, so neither a form post nor the component's state carries it.
                    --}}
                    <div wire:ignore x-data class="grid gap-3">
                        <label class="grid gap-1" x-show="$wire.rotationDone" x-cloak>
                            <span class="font-medium">{{ __('zimam.databases.rotate.password') }}</span>
                            <input id="db-password-{{ $rotationKey }}" type="text" readonly spellcheck="false" autocomplete="off" value="{{ $password ?? '' }}" class="block w-full rounded-lg border border-gray-300 bg-white p-3 font-mono text-sm dark:border-white/10 dark:bg-white/5">
                            <span class="text-gray-500 dark:text-gray-400">{{ __('zimam.databases.rotate.once') }}</span>
                        </label>
                    </div>
                    @if ($rotationDone)
                        <div class="grid gap-2">
                            <p class="text-gray-500 dark:text-gray-400">{{ __('zimam.databases.write_env.help') }}</p>
                            @if ($databases->isNotEmpty())
                                <label class="grid gap-1">
                                    <span class="font-medium">{{ __('zimam.databases.write_env.database') }}</span>
                                    <select wire:model="envDatabase" class="block w-full rounded-lg border border-gray-300 bg-white p-2 dark:border-white/10 dark:bg-white/5">
                                        @foreach ($databases as $database)
                                            <option value="{{ $database->name }}">{{ $database->name }}</option>
                                        @endforeach
                                    </select>
                                </label>
                            @endif
                            <div>
                                <x-filament::button color="gray" x-data x-on:click="$wire.writeEnv(document.getElementById('db-password-{{ $rotationKey }}').value)" :disabled="$envWritten">
                                    {{ __('zimam.databases.write_env.label') }}
                                </x-filament::button>
                            </div>
                        </div>
                    @endif
                    @error('envDatabase')
                        <p class="text-danger-600 dark:text-danger-400">{{ $message }}</p>
                    @enderror
                    @if ($envWritten)
                        <p>{{ __('zimam.databases.write_env.written') }}</p>
                    @endif
                    <div>
                        <x-filament::button color="gray" wire:click="closeRotation">
                            {{ __('zimam.databases.rotate.done') }}
                        </x-filament::button>
                    </div>
                </div>
            @else
                <div>{{ $this->rotateAction }}</div>
            @endif
        </div>
    </x-filament::section>

    <x-filament-actions::modals />
</div>
