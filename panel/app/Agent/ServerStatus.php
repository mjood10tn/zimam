<?php

namespace App\Agent;

use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use Carbon\CarbonImmutable;
use DateTimeZone;
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

    /**
     * The versions a site can run on: installed and still supported (§8.1),
     * labelled, the default marked.
     *
     * @return array<string, string> version => label
     */
    public function sitePhpOptions(): array
    {
        $php = $this->data['php'];
        $supported = array_column($php['available'], 'supported', 'version');
        $options = [];
        foreach ($php['installed'] as $installed) {
            $version = $installed['version'];
            if ($supported[$version] ?? false) {
                $options[$version] = $version === $php['default']
                    ? __('zimam.sites.fields.php_default', ['version' => $version])
                    : __('zimam.sites.fields.php_option', ['version' => $version]);
            }
        }

        return $options;
    }

    /**
     * What the status says of an optional service, or null when it says
     * nothing: an agent from before the service has no such key.
     *
     * @return array{installed: bool, version: string, active: bool}|null
     */
    public function service(string $name): ?array
    {
        return $this->data['services'][$name] ?? null;
    }

    /**
     * Whether the agent answered with a services list that leaves this one out:
     * it could not read it, so whether it is installed is unknown. A status with
     * no services at all is from an agent that predates them, and means none is
     * installed.
     */
    public function serviceUnknown(string $name): bool
    {
        return array_key_exists('services', $this->data) && ($this->data['services'][$name] ?? null) === null;
    }

    /**
     * The zone cron runs jobs in (ruling R7): the status's IANA name, or UTC
     * from an agent that predates it or one that names a zone PHP doesn't know.
     * Legacy aliases (US/Pacific, Asia/Calcutta, Etc/UTC) count: servers still use them.
     */
    public function timezone(): string
    {
        $zone = $this->data['timezone'] ?? 'UTC';

        return is_string($zone) && in_array($zone, DateTimeZone::listIdentifiers(DateTimeZone::ALL_WITH_BC), true) ? $zone : 'UTC';
    }

    /** When the next answer is due, for the bulletin's "Next" time. */
    public function nextAt(): CarbonImmutable
    {
        return $this->issuedAt->addSeconds(AgentHealth::TTL_SECONDS);
    }
}
