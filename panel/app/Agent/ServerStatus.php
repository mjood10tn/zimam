<?php

namespace App\Agent;

use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use Carbon\CarbonImmutable;
use Illuminate\Support\Facades\Cache;

/**
 * The server summary from system.status (§7.5), asked of the agent at most
 * every 10 seconds. The last good answer stays in the cache with its time,
 * so the dashboard still renders, marked stale, while the agent is down.
 */
final class ServerStatus
{
    private const KEY = 'zimam.system_status';

    /** @param array<string, mixed> $data system.status's data */
    private function __construct(
        public readonly array $data,
        public readonly CarbonImmutable $issuedAt,
        public readonly bool $fresh,
    ) {}

    /** Null until the agent has answered once. */
    public static function current(): ?self
    {
        $last = Cache::get(self::KEY);
        $issuedAt = is_array($last) ? CarbonImmutable::parse($last['issued_at']) : null;
        if (! app(AgentHealth::class)->isReachable()) {
            return $issuedAt === null ? null : new self($last['data'], $issuedAt, false);
        }
        if ($issuedAt !== null && $issuedAt->gt(now()->subSeconds(AgentHealth::TTL_SECONDS))) {
            return new self($last['data'], $issuedAt, true);
        }

        return self::fresh() ?? ($issuedAt === null ? null : new self($last['data'], $issuedAt, false));
    }

    /**
     * Asks the agent now, past the 10-second cache, and caches the answer:
     * after a job ends, the cached one predates it. Null if the agent
     * didn't answer.
     */
    public static function fresh(): ?self
    {
        try {
            $data = app(AgentClient::class)->call(new SystemStatus)->data;
        } catch (AgentException $e) {
            if (! $e instanceof AgentUnavailable) {
                report($e);
            }

            return null;
        }
        $issuedAt = CarbonImmutable::now();
        Cache::forever(self::KEY, ['data' => $data, 'issued_at' => $issuedAt->toIso8601String()]);

        return new self($data, $issuedAt, true);
    }

    /** When the next answer is due, for the bulletin's "Next" time. */
    public function nextAt(): CarbonImmutable
    {
        return $this->issuedAt->addSeconds(AgentHealth::TTL_SECONDS);
    }
}
