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
 * against the caller's address and the site name it used, the same for real
 * and made-up names. The name is the caller's choice, so that count alone
 * limits nothing: failures also count against the address by itself, which
 * is checked first, so one address can't keep the panel hashing by making
 * names up. That limit is generous, since GitHub or a shared CI runner
 * sends for many sites from one address and its own failures are rare.
 * Failures never count against the site, so a stranger can't hold back its
 * deliveries.
 *
 * Authenticated requests count against the site twice. Every one counts
 * toward a high limit, pings and ignored events included. Only a request
 * that is about to start a deploy counts toward the low one, six a minute
 * for both hooks together, so pushes to other branches can't use up the
 * budget of the push that deploys.
 */
final class HookGate
{
    /** Deploys the two hooks may start for one site. */
    public const PER_SITE_PER_MINUTE = 6;

    /** Authenticated requests of any kind for one site. */
    public const AUTHENTICATED_PER_MINUTE = 60;

    /** Failures of one address for one site name. */
    public const FAILURES_PER_MINUTE = 30;

    /** Failures of one address, whatever names it used. */
    public const ADDRESS_FAILURES_PER_MINUTE = 120;

    public function __construct(private readonly StartDeployment $start) {}

    /** Stops an address that failed too often lately, at all or for this name, before any lookup. */
    public function admit(Request $request, string $site): void
    {
        self::limit($this->addressKey($request), self::ADDRESS_FAILURES_PER_MINUTE);
        self::limit($this->failureKey($request, $site), self::FAILURES_PER_MINUTE);
    }

    /** The site's repository by the site's name, or null; nothing about which. */
    public function repository(string $site): ?SiteRepository
    {
        return Site::where('name', $site)->first()?->repository;
    }

    public function refuse(Request $request, string $site): never
    {
        RateLimiter::hit($this->addressKey($request), 60);
        RateLimiter::hit($this->failureKey($request, $site), 60);
        abort(404);
    }

    /** Counts an authenticated request of any kind against its site. */
    public function authenticated(SiteRepository $repository): void
    {
        $key = 'hooks:authenticated:'.$repository->site_id;
        self::limit($key, self::AUTHENTICATED_PER_MINUTE);
        RateLimiter::hit($key, 60);
    }

    /** Counts a request that is about to start a deploy against its site; call it right before deploy(). */
    public function throttle(SiteRepository $repository): void
    {
        $key = 'hooks:site:'.$repository->site_id;
        self::limit($key, self::PER_SITE_PER_MINUTE);
        RateLimiter::hit($key, 60);
    }

    private static function limit(string $key, int $perMinute): void
    {
        if (RateLimiter::tooManyAttempts($key, $perMinute)) {
            abort(429, '', ['Retry-After' => RateLimiter::availableIn($key)]);
        }
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

    private function failureKey(Request $request, string $site): string
    {
        return 'hooks:failures:'.sha1($request->ip().'|'.$site);
    }

    /**
     * An IPv6 caller usually holds a whole /64 and can send from any
     * address in it, so its network is what counts.
     */
    private function addressKey(Request $request): string
    {
        $ip = (string) $request->ip();
        $packed = @inet_pton($ip);
        if ($packed !== false && strlen($packed) === 16) {
            $ip = bin2hex(substr($packed, 0, 8));
        }

        return 'hooks:address:'.sha1($ip);
    }
}
