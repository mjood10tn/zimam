{{-- A destination's encryption keys as read-only fields to copy (R05-D3). $keys holds password and salt; $id makes the ids unique. --}}
<p>{{ __('zimam.backups.destinations.keys.store') }}</p>
@foreach (['password', 'salt'] as $which)
    <label class="grid gap-1" for="backup-{{ $which }}-{{ $id }}">
        <span class="font-medium">{{ __("zimam.backups.destinations.keys.{$which}") }}</span>
        <input id="backup-{{ $which }}-{{ $id }}" type="text" readonly spellcheck="false" autocomplete="off" value="{{ $keys[$which] }}" class="block w-full rounded-lg border border-gray-300 bg-white p-3 font-mono text-sm dark:border-white/10 dark:bg-white/5">
    </label>
@endforeach
