<div @unless ($finished) wire:poll.2s="poll" @endunless class="zimam-job-log">
    @if ($lines === [])
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ __('zimam.jobs.log.empty') }}</p>
    @else
        <pre class="overflow-x-auto whitespace-pre-wrap break-words rounded-lg bg-gray-950 p-4 font-mono text-xs leading-relaxed text-gray-100" role="log" aria-live="polite">@foreach ($lines as $line){{ $line }}
@endforeach</pre>
    @endif
    @unless ($finished)
        <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ __('zimam.jobs.log.live') }}</p>
    @endunless
</div>
