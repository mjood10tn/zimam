{{--
    The new password of a site's service user, shown once, and writing it to .env. Shared by the Database and
    Cache tabs; $lang names the language group. The password is in no request but the one that made it, and the
    one that writes it to .env. $envFields may name a view with more fields for the .env write.
--}}
<div class="grid gap-3">
    @if ($rotationFailure !== null)
        <p class="text-danger-600 dark:text-danger-400">{{ $rotationFailure }}</p>
    @endif

    @if ($rotationKey !== null)
        {{-- Polls only while the job runs; the poll's responses carry no password. --}}
        <div wire:key="rotation-{{ $rotationKey }}" @if ($pending) wire:poll.3s="refreshRotation" @endif class="grid gap-3">
            <p>{{ $rotationDone ? __("zimam.{$lang}.rotate.ready") : __("zimam.{$lang}.rotate.pending") }}</p>
            {{--
                The password is rendered only in the response that made it. wire:ignore keeps the
                browser's copy through later requests, whose HTML has none; it is hidden until the job
                has succeeded, and goes back to the server only as writeEnv()'s argument. No name and no
                wire:model, so neither a form post nor the component's state carries it.
            --}}
            <div wire:ignore x-data class="grid gap-3">
                <label class="grid gap-1" x-show="$wire.rotationDone" x-cloak>
                    <span class="font-medium">{{ __("zimam.{$lang}.rotate.password") }}</span>
                    <input id="service-password-{{ $rotationKey }}" type="text" readonly spellcheck="false" autocomplete="off" value="{{ $password ?? '' }}" class="block w-full rounded-lg border border-gray-300 bg-white p-3 font-mono text-sm dark:border-white/10 dark:bg-white/5">
                    <span class="text-gray-500 dark:text-gray-400">{{ __("zimam.{$lang}.rotate.once") }}</span>
                </label>
            </div>
            @if ($rotationDone)
                <div class="grid gap-2">
                    <p class="text-gray-500 dark:text-gray-400">{{ __("zimam.{$lang}.write_env.help") }}</p>
                    @isset($envFields)
                        @include($envFields)
                    @endisset
                    <div>
                        <x-filament::button color="gray" x-data x-on:click="$wire.writeEnv(document.getElementById('service-password-{{ $rotationKey }}').value)" :disabled="$envWritten">
                            {{ __("zimam.{$lang}.write_env.label") }}
                        </x-filament::button>
                    </div>
                </div>
            @endif
            @if ($envWritten)
                <p>{{ __("zimam.{$lang}.write_env.written") }}</p>
            @endif
            <div>
                <x-filament::button color="gray" wire:click="closeRotation">
                    {{ __("zimam.{$lang}.rotate.done") }}
                </x-filament::button>
            </div>
        </div>
    @else
        <div>{{ $this->rotateAction }}</div>
    @endif
</div>
