<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Refuses a hook request whose body is over GitHub's largest delivery, 25 MiB
 * (A10), before anything reads it as JSON or checks a signature over it. The
 * same for every site name, so the answer tells nothing about which exist.
 */
final class LimitHookBody
{
    public const MAX_BYTES = 25 * 1024 * 1024;

    public function handle(Request $request, Closure $next): Response
    {
        $declared = $request->server('CONTENT_LENGTH');
        if (is_numeric($declared) && (int) $declared > self::MAX_BYTES) {
            abort(413);
        }
        if (strlen($request->getContent()) > self::MAX_BYTES) {
            abort(413);
        }

        return $next($request);
    }
}
