<?php

namespace App\Agent;

use App\Agent\Commands\SystemMetrics;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use Carbon\CarbonImmutable;
use Illuminate\Support\Facades\Cache;

/**
 * The live system.metrics answer (§7.5), asked of the agent at most every
 * 4 seconds, so the Monitoring page's widgets, which poll every 5, and the
 * dashboard share one call. The last good answer stays with its time, so
 * the pages still render, marked stale, while the agent is down.
 */
final class MetricsReading
{
    private const KEY = 'zimam.system_metrics';

    public const TTL_SECONDS = 4;

    /** @param array<string, mixed> $data system.metrics's data */
    private function __construct(
        public readonly array $data,
        public readonly CarbonImmutable $at,
        public readonly bool $fresh,
    ) {}

    /** Null until the agent has answered once. */
    public static function current(): ?self
    {
        $last = Cache::get(self::KEY);
        $at = is_array($last) ? CarbonImmutable::parse($last['at']) : null;
        if (! app(AgentHealth::class)->isReachable()) {
            return $at === null ? null : new self($last['data'], $at, false);
        }
        if ($at !== null && $at->gt(now()->subSeconds(self::TTL_SECONDS))) {
            return new self($last['data'], $at, true);
        }
        try {
            $data = app(AgentClient::class)->call(new SystemMetrics)->data;
        } catch (AgentException $e) {
            if (! $e instanceof AgentUnavailable) {
                report($e);
            }

            return $at === null ? null : new self($last['data'], $at, false);
        }
        $now = CarbonImmutable::now();
        Cache::forever(self::KEY, ['data' => $data, 'at' => $now->toIso8601String()]);

        return new self($data, $now, true);
    }
}
