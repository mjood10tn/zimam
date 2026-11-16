<?php

use App\Http\Middleware\EnforcePanelIpAllowlist;
use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Route;

return Application::configure(basePath: dirname(__DIR__))
    ->withRouting(
        web: __DIR__.'/../routes/web.php',
        commands: __DIR__.'/../routes/console.php',
        health: '/up',
        // GitHub's webhook and the deploy URL: outside the web group, so
        // they get no session, cookies or CSRF check (R02-D11).
        then: fn () => Route::group([], __DIR__.'/../routes/hooks.php'),
    )
    ->withMiddleware(function (Middleware $middleware): void {
        // The panel IP allowlist (§7.2) guards every request first, the
        // setup page and Livewire's update requests included; only the
        // hooks, which authenticate by signature or token, are exempt (OQ2).
        $middleware->prepend(EnforcePanelIpAllowlist::class);
    })
    ->withExceptions(function (Exceptions $exceptions): void {
        $exceptions->shouldRenderJsonWhen(
            fn (Request $request) => $request->is('api/*') || $request->expectsJson(),
        );
    })->create();
