<?php

use App\Http\Controllers\Hooks\DeployUrlController;
use App\Http\Controllers\Hooks\GithubWebhookController;
use App\Http\Middleware\LimitHookBody;
use App\Models\Site;
use Illuminate\Support\Facades\Route;

/*
 * GitHub's webhook and the deploy URL (R02-D11). Outside the web group: no
 * session, cookies or CSRF token, since GitHub and a deploy script have
 * none. They authenticate by signature or token, so the panel IP allowlist
 * leaves them out (OQ2) and a throttle guards them instead. A path whose
 * site part can't be a site name matches no route: it gets the 404 before
 * any limiter, database or HMAC work.
 */
Route::middleware(LimitHookBody::class)->prefix('hooks')->where(['site' => Site::NAME_SHAPE])->group(function (): void {
    Route::post('github/{site}', GithubWebhookController::class);
    Route::post('deploy/{site}/{token}', DeployUrlController::class);
});
