<?php

namespace Tests\Feature\Platform;

use Illuminate\Contracts\Http\Kernel;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Http\Middleware\TrustProxies;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Route;
use Tests\TestCase;

/**
 * The panel trusts no proxy header (R06-D8): in Cloudflare mode Nginx's
 * realip already makes REMOTE_ADDR the visitor's address, so the allowlist,
 * the login throttle and fail2ban see it without the panel reading a header
 * anyone could send.
 */
final class ClientIpTest extends TestCase
{
    use RefreshDatabase;

    protected function tearDown(): void
    {
        TrustProxies::flushState();
        parent::tearDown();
    }

    public function testClientIpIsRemoteAddr(): void
    {
        // Even told to trust every proxy, the panel never runs the middleware.
        TrustProxies::at('*');
        Route::get('/client-ip-test', fn (Request $request) => $request->ip());

        $this->withServerVariables(['REMOTE_ADDR' => '203.0.113.9'])
            ->withHeaders(['X-Forwarded-For' => '198.51.100.1', 'CF-Connecting-IP' => '198.51.100.2'])
            ->get('/client-ip-test')
            ->assertOk()
            ->assertContent('203.0.113.9');
    }

    public function testTrustProxiesNotInStack(): void
    {
        $this->assertNotContains(TrustProxies::class, $this->app->make(Kernel::class)->getGlobalMiddleware());
    }
}
