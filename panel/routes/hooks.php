<?php

use App\Http\Controllers\Hooks\DeployUrlController;
use App\Http\Controllers\Hooks\GithubWebhookController;
use App\Http\Middleware\LimitHookBody;
use Illuminate\Support\Facades\Route;

/*
 * GitHub's webhook and the deploy URL (R02-D11). Outside the web group: no
 * session, cookies or CSRF token, since GitHub and a deploy script have
 * none. They authenticate by signature or token, so the panel IP allowlist
 * leaves them out (OQ2) and a throttle guards them instead.
 */
Route::middleware(LimitHookBody::class)->prefix('hooks')->group(function (): void {
    Route::post('github/{site}', GithubWebhookController::class);
    Route::post('deploy/{site}/{token}', DeployUrlController::class);
});
