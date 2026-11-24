{{--
    Time left before the agent's timer reverts the change. Counted from the deadline on each tick, so a
    throttled background tab never drifts; at zero the component settles and closes the modal. role="timer"
    is not announced every second.
--}}
@if ($deadline !== null)
    @php($left = max(0, (int) ceil(now()->diffInSeconds($deadline, false))))
    <p
        class="text-base"
        x-data="{
            deadline: {{ $deadline->getTimestampMs() }},
            left: {{ $left }},
            tick() {
                this.left = Math.max(0, Math.ceil((this.deadline - Date.now()) / 1000))
                if (this.left === 0) {
                    clearInterval(this.timer)
                    $wire.settle()
                }
            },
            init() { this.timer = setInterval(() => this.tick(), 1000) },
            destroy() { clearInterval(this.timer) },
        }"
    >
        {{ __('zimam.server_security.change.countdown') }}
        <span
            role="timer"
            class="font-bold tabular-nums"
            x-text="Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0')"
        >{{ intdiv($left, 60) }}:{{ str_pad((string) ($left % 60), 2, '0', STR_PAD_LEFT) }}</span>
    </p>
@endif
