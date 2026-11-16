<?php

namespace App\Http\Controllers\Hooks;

use App\Models\Deployment;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use SensitiveParameter;

/**
 * POST /hooks/deploy/{site}/{token}: deploys the site's branch (R02-D11). The
 * URL is the request, so auto-deploy doesn't apply; the body is ignored.
 */
final class DeployUrlController
{
    public function __invoke(Request $request, HookGate $gate, string $site, #[SensitiveParameter] string $token): JsonResponse
    {
        $gate->admit($request);
        $repository = $gate->repository($site);
        if ($repository === null || ! $repository->matchesDeployToken($token)) {
            $gate->refuse($request);
        }
        $gate->throttle($repository);

        return $gate->deploy($repository, Deployment::URL);
    }
}
