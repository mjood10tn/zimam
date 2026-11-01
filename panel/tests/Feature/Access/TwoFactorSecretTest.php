<?php

namespace Tests\Feature\Access;

use App\Filament\Auth\LongSecretAppAuthentication;
use Filament\Facades\Filament;
use Tests\TestCase;

/** 2FA is required, with authenticator apps and recovery codes (§7.2). */
final class TwoFactorSecretTest extends TestCase
{
    /** 32 base32 characters, 160 bits, rather than Filament's 80 (Task 3's owner decision). */
    public function testSecretIs160Bits(): void
    {
        $secret = LongSecretAppAuthentication::make()->generateSecret();

        $this->assertMatchesRegularExpression('/^[A-Z2-7]{32}\z/', $secret);
    }

    public function testPanelRequires2fa(): void
    {
        $panel = Filament::getPanel('admin');

        $this->assertTrue($panel->isMultiFactorAuthenticationRequired());
        $providers = array_values($panel->getMultiFactorAuthenticationProviders());
        $this->assertCount(1, $providers);
        $this->assertInstanceOf(LongSecretAppAuthentication::class, $providers[0]);
        $this->assertTrue($providers[0]->isRecoverable());
    }
}
