<?php

namespace App\Http\Controllers\Hooks;

use App\Actions\StartDeployment;
use App\Agent\Exceptions\AgentException;
use App\Models\Deployment;
use App\Models\Site;
use App\Models\SiteRepository;
use Filament\Support\Exceptions\Halt;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\RateLimiter;

/**
 * What the GitHub webhook and the deploy URL share (R02-D11).
 *
 * A caller that can't sign or doesn't hold the token gets the framework's
 * 404 whatever went wrong: no site, no repository, a bad token or signature.
 * Only after that may an answer say anything about the site. Failures count
 * against the caller's address, the same for every site name, so one address
 * can't keep the panel hashing; they don't count against the site, so a
 * stranger can't use up its throttle. Authenticated requests count against
 * the site: six a minute, both hooks together.
 */
final class HookGate
{
    public const PER_SITE_PER_MINUTE = 6;

    public const FAILURES_PER_MINUTE = 30;

    public function __construct(private readonly StartDeployment $start) {}

    /** Stops an address that failed too often lately, before any lookup. */
    public function admit(Request $request): void
    {
        $key = $this->failureKey($request);
        if (RateLimiter::tooManyAttempts($key, self::FAILURES_PER_MINUTE)) {
            abort(429, '', ['Retry-After' => RateLimiter::availableIn($key)]);
        }
    }

    /** The site's repository by the site's name, or null; nothing about which. */
    public function repository(string $site): ?SiteRepository
    {
        return Site::where('name', $site)->first()?->repository;
    }

    public function refuse(Request $request): never
    {
        RateLimiter::hit($this->failureKey($request), 60);
        abort(404);
    }

    /** Counts an authenticated request against its site. */
    public function throttle(SiteRepository $repository): void
    {
        $key = 'hooks:site:'.$repository->site_id;
        if (RateLimiter::tooManyAttempts($key, self::PER_SITE_PER_MINUTE)) {
            abort(429, '', ['Retry-After' => RateLimiter::availableIn($key)]);
        }
        RateLimiter::hit($key, 60);
    }

    /**
     * Starts the deploy and answers for it. There's no one to show a
     * notification to here, so the agent being away or refusing is answered
     * with a status the caller (GitHub's delivery log, a script) can read.
     */
    public function deploy(SiteRepository $repository, string $trigger, ?string $commit = null): JsonResponse
    {
        if (! $repository->isConnected()) {
            return self::answer(409, 'not_connected');
        }
        try {
            $deployment = $this->start->run($repository->site, $trigger, $commit);
        } catch (Halt) {
            return self::answer(503, 'unavailable');
        } catch (AgentException $e) {
            report($e);

            return self::answer(502, 'refused');
        }

        return self::answer(202, $deployment->status === Deployment::WAITING ? 'waiting' : 'deploying');
    }

    public static function answer(int $status, string $key): JsonResponse
    {
        return new JsonResponse(['message' => __("zimam.hooks.{$key}")], $status);
    }

    private function failureKey(Request $request): string
    {
        return 'hooks:failures:'.$request->ip();
    }
}
