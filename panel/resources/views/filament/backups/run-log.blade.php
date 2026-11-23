{{-- The end of a backup run's log (R05-D9), or why there is none. --}}
@if ($log === null)
    <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.backups.runs.no_log') }}</p>
@else
    <pre class="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words text-xs">{{ $log }}</pre>
@endif
