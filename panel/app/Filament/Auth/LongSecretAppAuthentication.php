<?php

namespace App\Filament\Auth;

use Filament\Auth\MultiFactor\App\AppAuthentication;

/** Authenticator-app 2FA with 160-bit secrets (32 base32 characters), not Filament's 80 (Task 3's owner decision). */
class LongSecretAppAuthentication extends AppAuthentication
{
    public function generateSecret(): string
    {
        return $this->google2FA->generateSecretKey(32);
    }
}
