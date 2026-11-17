<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Keeps the browser from storing the panel's pages. The back button would
 * otherwise bring back a page as it was, with a one-time password or a
 * revealed secret still in it. Static assets don't pass through the panel.
 */
final class NoStorePanelPages
{
    public function handle(Request $request, Closure $next): Response
    {
        $response = $next($request);
        $response->headers->set('Cache-Control', 'no-store, private');

        return $response;
    }
}
