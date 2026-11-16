<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Refuses a hook request whose body is over 1 MiB, before anything reads it
 * as JSON or checks a signature over it. That is the panel vhost's Nginx
 * limit (its default), so tests and production agree; GitHub can send up to
 * 25 MiB, and the webhook setup text says to use Deploy now or the deploy
 * URL for larger pushes. The same for every site name, so the answer tells
 * nothing about which exist.
 */
final class LimitHookBody
{
    public const MAX_BYTES = 1024 * 1024;

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
