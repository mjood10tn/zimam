<?php

namespace Tests\Feature\Hooks;

use App\Http\Middleware\LimitHookBody;
use App\Models\Deployment;
use App\Settings\PanelSettings;
use Illuminate\Cookie\Middleware\EncryptCookies;
use Illuminate\Foundation\Http\Middleware\ValidateCsrfToken;
use Illuminate\Foundation\Http\Middleware\VerifyCsrfToken;
use Illuminate\Session\Middleware\StartSession;
use Illuminate\Support\Facades\Route;

/**
 * What both hooks share (R02-D11, OQ2): a throttle per site, no session, and
 * no panel IP allowlist, since they authenticate by signature or token.
 */
final class HookAccessTest extends HooksTestCase
{
    public function testThrottleAfterSixPerMinute(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $token = $repository->regenerateDeployToken();

        for ($i = 0; $i < 3; $i++) {
            $this->postRaw("/hooks/deploy/shop/{$token}", '')->assertStatus(202);
            $this->deliver('shop', $repository->webhook_secret, 'ping', ['zen' => 'z'])->assertOk();
        }
        // The seventh in the minute, whichever hook it comes to.
        $this->postRaw("/hooks/deploy/shop/{$token}", '')->assertStatus(429);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push())->assertStatus(429);
        $this->assertSame(3, Deployment::count());

        // Another site has its own count.
        $other = $this->connected($this->site(name: 'blog'));
        $this->postRaw('/hooks/deploy/blog/'.$other->regenerateDeployToken(), '')->assertStatus(202);

        // A minute later it is open again.
        $this->travel(61)->seconds();
        $this->postRaw("/hooks/deploy/shop/{$token}", '')->assertStatus(202);
    }

    /** Requests that fail to authenticate don't use up a site's throttle, so a stranger can't hold its deploys back. */
    public function testStrangersDontSpendTheSitesThrottle(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $token = $repository->regenerateDeployToken();

        for ($i = 0; $i < 10; $i++) {
            $this->postRaw('/hooks/deploy/shop/'.str_repeat('0', 64), '', ip: '198.51.100.'.$i)->assertNotFound();
            $this->deliver('shop', 'not the secret', 'push', $this->push(), ip: '198.51.100.'.$i)->assertNotFound();
        }

        $this->postRaw("/hooks/deploy/shop/{$token}", '')->assertStatus(202);
    }

    /**
     * Failures are counted per address and site name. A made-up name is
     * throttled exactly like a real one, so the answer tells nothing.
     */
    public function testFailuresThrottledPerAddressAndName(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $token = $repository->regenerateDeployToken();

        foreach (['shop', 'nosuchsite'] as $name) {
            for ($i = 0; $i < 30; $i++) {
                $this->postRaw("/hooks/deploy/{$name}/".str_repeat('0', 64), '', ip: '198.51.100.7')->assertNotFound();
            }
            $this->postRaw("/hooks/deploy/{$name}/".str_repeat('0', 64), '', ip: '198.51.100.7')->assertStatus(429);
            $this->deliver($name, 'x', 'push', $this->push(), ip: '198.51.100.7')->assertStatus(429);
        }

        $this->postRaw("/hooks/deploy/shop/{$token}", '', ip: '198.51.100.7')->assertStatus(429);
        // Other addresses are unaffected.
        $this->postRaw("/hooks/deploy/shop/{$token}", '', ip: '203.0.113.9')->assertStatus(202);
    }

    /**
     * GitHub, or a shared CI runner, sends for many sites from one address:
     * failures for one site name don't hold back another site's deliveries.
     */
    public function testFailuresForOneNameDontBlockAnother(): void
    {
        $this->connected($this->site(name: 'shopx'), ['auto_deploy' => true]);
        $y = $this->connected($this->site(name: 'shopy'), ['auto_deploy' => true]);

        for ($i = 0; $i < 30; $i++) {
            $this->deliver('shopx', 'not the secret', 'push', $this->push(), ip: '140.82.115.1')->assertNotFound();
        }
        $this->deliver('shopx', 'not the secret', 'push', $this->push(), ip: '140.82.115.1')->assertStatus(429);

        $this->deliver('shopy', $y->webhook_secret, 'push', $this->push(), ip: '140.82.115.1')->assertStatus(202);
        $this->postRaw('/hooks/deploy/shopy/'.$y->regenerateDeployToken(), '', ip: '140.82.115.1')->assertStatus(202);
    }

    public function testHooksBypassAllowlist(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $token = $repository->regenerateDeployToken();
        app(PanelSettings::class)->setPanelIpAllowlist(['203.0.113.0/24']);

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ip: '198.51.100.7')->assertStatus(202);
        $this->postRaw("/hooks/deploy/shop/{$token}", '', ip: '198.51.100.7')->assertStatus(202);
        // The exemption is for the hooks alone.
        $this->postRaw('/hooksx', '', ip: '198.51.100.7')->assertForbidden();
        $this->withServerVariables(['REMOTE_ADDR' => '198.51.100.7'])->get('/admin/login')->assertForbidden();
        $this->withServerVariables(['REMOTE_ADDR' => '198.51.100.7'])->get('/hooks')->assertForbidden();
    }

    /** The webhook setup text names the 1 MiB limit and what to do instead. */
    public function testSetupTextSaysLargePushesAreRefused(): void
    {
        $text = __('zimam.deployments.webhook.setup', ['url' => 'https://panel.example.com/hooks/github/shop']);

        $this->assertSame(1024 * 1024, LimitHookBody::MAX_BYTES);
        $this->assertStringContainsString('1 MiB', $text);
        $this->assertStringContainsString(__('zimam.deployments.deploy.label'), $text);
    }

    public function testSettingsSayHooksAreExempt(): void
    {
        $this->assertStringContainsString('/hooks/', __('zimam.settings.help.ip_allowlist'));
    }

    public function testHookResponsesCarryNoSessionCookie(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $token = $repository->regenerateDeployToken();

        $responses = [
            $this->deliver('shop', $repository->webhook_secret, 'push', $this->push()),
            $this->deliver('shop', $repository->webhook_secret, 'ping', ['zen' => 'z']),
            $this->deliver('shop', 'not the secret', 'push', $this->push()),
            $this->postRaw("/hooks/deploy/shop/{$token}", ''),
            $this->postRaw('/hooks/deploy/nosuchsite/x', ''),
        ];

        foreach ($responses as $response) {
            $this->assertSame([], $response->headers->getCookies());
            $this->assertNull($response->headers->get('Set-Cookie'));
        }
    }

    /**
     * The hooks are outside the web group: no session, cookies or CSRF check.
     * (The tests skip CSRF anyway, so this looks at the routes themselves.)
     */
    public function testHookRoutesHaveNoWebMiddleware(): void
    {
        foreach (['hooks/github/{site}', 'hooks/deploy/{site}/{token}'] as $uri) {
            $route = collect(Route::getRoutes()->getRoutes())->first(fn ($r) => $r->uri() === $uri);
            $this->assertNotNull($route, $uri);
            $this->assertSame(['POST'], $route->methods());
            $middleware = Route::gatherRouteMiddleware($route);
            $this->assertNotContains('web', $route->gatherMiddleware());
            foreach ([StartSession::class, ValidateCsrfToken::class, VerifyCsrfToken::class, EncryptCookies::class] as $class) {
                $this->assertNotContains($class, $middleware, $uri);
            }
        }
    }
}
