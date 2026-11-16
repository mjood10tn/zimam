<?php

namespace App\Http\Controllers\Hooks;

use App\Models\Deployment;
use App\Models\SiteRepository;
use App\Models\WebhookDelivery;
use Illuminate\Database\UniqueConstraintViolationException;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Throwable;

/**
 * POST /hooks/github/{site}: GitHub's webhook (R02-D11, A10). The signature
 * is checked first, over the raw body, whatever its content type, so only a
 * caller holding the secret learns anything: that the content type must be
 * application/json (415), that a push was ignored (202) or malformed (400).
 * A push to the site's branch, with auto-deploy on, deploys its commit; the
 * answer comes once the job is submitted, well inside GitHub's 10 seconds.
 */
final class GithubWebhookController
{
    private const COMMIT_PATTERN = '/^[0-9a-f]{40}\z/';

    public function __invoke(Request $request, HookGate $gate, string $site): JsonResponse
    {
        $gate->admit($request, $site);
        $body = $request->getContent();
        $repository = $gate->repository($site);
        $signature = (string) $request->header('X-Hub-Signature-256', '');
        // An unknown site gets its HMAC worked out too, so it takes as long.
        $expected = 'sha256='.hash_hmac('sha256', $body, $repository?->webhook_secret ?? random_bytes(32));
        if ($repository === null || ! hash_equals($expected, $signature)) {
            $gate->refuse($request, $site);
        }
        $gate->throttle($repository);

        if (! self::isJson($request)) {
            return HookGate::answer(415, 'json_only');
        }

        return match ($request->header('X-GitHub-Event')) {
            'ping' => HookGate::answer(200, 'pong'),
            'push' => $this->push($request, $gate, $repository, $body),
            default => HookGate::answer(202, 'ignored_event'),
        };
    }

    private function push(Request $request, HookGate $gate, SiteRepository $repository, string $body): JsonResponse
    {
        $delivery = (string) $request->header('X-GitHub-Delivery', '');
        $payload = json_decode($body, true, 64);
        if ($delivery === '' || strlen($delivery) > 255 || ! is_array($payload) || ! is_string($payload['ref'] ?? null)) {
            return HookGate::answer(400, 'malformed');
        }
        if (($payload['deleted'] ?? false) === true) {
            return HookGate::answer(202, 'ignored_deleted');
        }
        if ($payload['ref'] !== 'refs/heads/'.$repository->branch) {
            return HookGate::answer(202, 'ignored_branch');
        }
        if (! $repository->auto_deploy) {
            return HookGate::answer(202, 'ignored_auto_deploy');
        }
        $commit = $payload['after'] ?? null;
        if (! is_string($commit) || preg_match(self::COMMIT_PATTERN, $commit) !== 1) {
            return HookGate::answer(400, 'malformed');
        }
        if (! $repository->isConnected()) {
            return HookGate::answer(409, 'not_connected');
        }

        // A redelivery of one already deployed is answered and dropped.
        try {
            $seen = WebhookDelivery::create(['delivery_id' => $delivery, 'site_id' => $repository->site_id, 'received_at' => now()]);
        } catch (UniqueConstraintViolationException) {
            return HookGate::answer(202, 'duplicate');
        }
        try {
            $response = $gate->deploy($repository, Deployment::WEBHOOK, $commit);
        } catch (Throwable $e) {
            // Nothing was deployed, so GitHub's redelivery may try again.
            $seen->delete();

            throw $e;
        }
        if ($response->getStatusCode() !== 202) {
            $seen->delete();
        }

        return $response;
    }

    private static function isJson(Request $request): bool
    {
        $type = strtolower(trim(explode(';', (string) $request->header('Content-Type', ''))[0]));

        return $type === 'application/json';
    }
}
