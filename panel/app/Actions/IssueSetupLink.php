<?php

namespace App\Actions;

use App\Models\SetupToken;
use Illuminate\Support\Facades\DB;

/**
 * Issues a one-time setup link (design §4) and invalidates every unused
 * one. With no admin yet the link creates it; with one, it resets its
 * password and 2FA.
 */
final class IssueSetupLink
{
    public const VALID_MINUTES = 60;

    /** @return string https://<APP_URL's host>/setup/<token> */
    public function __invoke(): string
    {
        // 32 random bytes as unpadded base64url: 43 characters.
        $token = rtrim(strtr(base64_encode(random_bytes(32)), '+/', '-_'), '=');

        DB::transaction(function () use ($token): void {
            SetupToken::query()->whereNull('used_at')->delete();
            SetupToken::query()->create([
                'token_hash' => hash('sha256', $token),
                'expires_at' => now()->addMinutes(self::VALID_MINUTES),
            ]);
        });

        return 'https://'.parse_url((string) config('app.url'), PHP_URL_HOST).'/setup/'.$token;
    }
}
