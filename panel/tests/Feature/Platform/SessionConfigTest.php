<?php

namespace Tests\Feature\Platform;

use Tests\Support\EvaluatesConfigFiles;
use Tests\TestCase;

final class SessionConfigTest extends TestCase
{
    use EvaluatesConfigFiles;

    /** §7.2: sessions expire after 2 hours idle, not when the browser closes. */
    public function testSessionIdleTimeout120(): void
    {
        $session = $this->evaluateConfig('session.php', ['SESSION_LIFETIME' => null, 'SESSION_EXPIRE_ON_CLOSE' => null]);
        $this->assertSame(120, $session['lifetime']);
        $this->assertFalse($session['expire_on_close']);
        $this->assertMatchesRegularExpression('/^SESSION_LIFETIME=120$/m', file_get_contents(base_path('.env.example')));
    }

    /**
     * In production the panel is only served over HTTPS (bootstrap switches
     * its vhost before printing the setup link), so the session cookie never
     * travels in the clear. Herd serves plain HTTP locally.
     */
    public function testSecureCookieInProduction(): void
    {
        $production = $this->evaluateConfig('session.php', ['APP_ENV' => 'production', 'SESSION_SECURE_COOKIE' => null]);
        $this->assertTrue($production['secure']);

        $local = $this->evaluateConfig('session.php', ['APP_ENV' => 'local', 'SESSION_SECURE_COOKIE' => null]);
        $this->assertFalse((bool) $local['secure']);
    }
}
