<?php

namespace App\Agent;

use App\Agent\Commands\AgentVersion;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use Carbon\CarbonImmutable;
use Closure;
use Filament\Actions\Action;
use Illuminate\Support\Facades\Cache;

/**
 * Whether the agent answers: an agent.version call cached for 10 seconds
 * (D10), either way, so a dead agent isn't asked on every page. Bound
 * scoped, so one request asks the cache once.
 */
final class AgentHealth
{
    public const TTL_SECONDS = 10;

    private const REACHABLE_KEY = 'zimam.agent.reachable';

    private const LAST_ANSWER_KEY = 'zimam.agent.last_answer';

    private ?bool $reachable = null;

    public function __construct(private readonly AgentClient $agent) {}

    public function isReachable(): bool
    {
        return $this->reachable ??= Cache::remember(self::REACHABLE_KEY, self::TTL_SECONDS, function (): bool {
            try {
                $this->agent->call(new AgentVersion);
            } catch (AgentException $e) {
                if (! $e instanceof AgentUnavailable) {
                    report($e);
                }

                return false;
            }
            Cache::forever(self::LAST_ANSWER_KEY, now()->toIso8601String());

            return true;
        });
    }

    /** When the agent last answered, if it ever did. */
    public function lastAnswer(): ?CarbonImmutable
    {
        $at = Cache::get(self::LAST_ANSWER_KEY);

        return is_string($at) ? CarbonImmutable::parse($at) : null;
    }

    /**
     * Disables an action that calls the agent while the agent is unreachable,
     * and says why. $blocker, evaluated like any action closure (with the
     * $record, say), returns another reason to disable it, or null.
     * Filament keeps one disabled() condition, so both live here.
     */
    public static function guard(Action $action, ?Closure $blocker = null): Action
    {
        $reason = fn (Action $action): ?string => app(self::class)->isReachable()
            ? ($blocker === null ? null : $action->evaluate($blocker))
            : __('zimam.agent.paused');

        return $action
            ->disabled(fn (Action $action): bool => $reason($action) !== null)
            ->tooltip(fn (Action $action): ?string => $reason($action));
    }
}
