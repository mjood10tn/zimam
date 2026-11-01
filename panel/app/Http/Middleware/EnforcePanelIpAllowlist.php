<?php

namespace App\Http\Middleware;

use App\Settings\PanelSettings;
use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\IpUtils;
use Symfony\Component\HttpFoundation\Response;

final class EnforcePanelIpAllowlist
{
    public function __construct(private PanelSettings $settings) {}

    public function handle(Request $request, Closure $next): Response
    {
        $allowlist = $this->settings->panelIpAllowlist();

        // getClientIp() is the TCP peer: no proxy is trusted (global rule 13).
        if ($allowlist !== [] && ! IpUtils::checkIp((string) $request->getClientIp(), $allowlist)) {
            abort(403);
        }

        return $next($request);
    }
}
