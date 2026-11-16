<?php

namespace Tests\Feature\Hooks;

use App\Http\Controllers\Hooks\HookGate;
use App\Http\Middleware\LimitHookBody;
use App\Models\Deployment;
use App\Models\Site;
use App\Models\WebhookDelivery;
use App\Settings\PanelSettings;
use Illuminate\Console\Scheduling\Schedule;
use Illuminate\Cookie\Middleware\EncryptCookies;
use Illuminate\Foundation\Http\Middleware\ValidateCsrfToken;
use Illuminate\Foundation\Http\Middleware\VerifyCsrfToken;
use Illuminate\Session\Middleware\StartSession;
use Illuminate\Support\Facades\DB;
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
            $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(commit: str_repeat((string) $i, 40)))->assertStatus(202);
        }
        // The seventh deploy in the minute, whichever hook it comes to.
        $this->postRaw("/hooks/deploy/shop/{$token}", '')->assertStatus(429);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['X-GitHub-Delivery' => 'd-7'])->assertStatus(429);
        // The refused push's delivery isn't kept: its redelivery may deploy.
        $this->assertFalse(WebhookDelivery::where('delivery_id', 'd-7')->exists());

        // Another site has its own count.
        $other = $this->connected($this->site(name: 'blog'));
        $this->postRaw('/hooks/deploy/blog/'.$other->regenerateDeployToken(), '')->assertStatus(202);

        // A minute later it is open again.
        $this->travel(61)->seconds();
        $this->postRaw("/hooks/deploy/shop/{$token}", '')->assertStatus(202);
    }

    /**
     * Only a request that starts a deploy counts toward the six a
     * minute. Pings, other events and pushes to other branches don't, so a
     * busy repository can't use up the budget of the push that deploys.
     */
    public function testIgnoredEventsDontSpendTheDeployThrottle(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);

        for ($i = 0; $i < 4; $i++) {
            $this->deliver('shop', $repository->webhook_secret, 'ping', ['zen' => 'z'])->assertOk();
            $this->deliver('shop', $repository->webhook_secret, 'push', $this->push('feature-'.$i))->assertStatus(202);
            $this->deliver('shop', $repository->webhook_secret, 'issues', ['action' => 'opened'])->assertStatus(202);
        }
        $this->assertSame(0, Deployment::count());

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push())->assertStatus(202);
        $this->assertSame(1, Deployment::count());
    }

    /** Authenticated requests of any kind still have a limit of their own, a higher one. */
    public function testAuthenticatedRequestsHaveAHigherLimit(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);

        for ($i = 0; $i < HookGate::AUTHENTICATED_PER_MINUTE; $i++) {
            $this->deliver('shop', $repository->webhook_secret, 'ping', ['zen' => 'z'])->assertOk();
        }
        $this->deliver('shop', $repository->webhook_secret, 'ping', ['zen' => 'z'])->assertStatus(429);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push())->assertStatus(429);
        $this->assertSame(0, Deployment::count());

        $this->travel(61)->seconds();
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push())->assertStatus(202);
    }

    /**
     * A path whose site part can't be a site name matches no route.
     * It is answered before any limiter or database work.
     */
    public function testImpossibleSiteNamesMatchNoRoute(): void
    {
        $this->connected($this->site(), ['auto_deploy' => true]);
        DB::enableQueryLog();

        foreach (['Shop', 'a', 'shop.', 'shop_x', '-shop', 'shop-', str_repeat('a', 25), 'sh%20op'] as $name) {
            $this->postRaw("/hooks/github/{$name}", '{}')->assertNotFound();
            $this->postRaw("/hooks/deploy/{$name}/".str_repeat('0', 64), '')->assertNotFound();
        }

        $this->assertSame([], DB::getQueryLog());
        DB::disableQueryLog();
        foreach (Route::getRoutes() as $route) {
            if (str_starts_with($route->uri(), 'hooks/')) {
                $this->assertSame(Site::NAME_SHAPE, $route->wheres['site'] ?? null, $route->uri());
            }
        }
    }

    /**
     * The site name is the caller's choice, so failures also count
     * against the address alone. A new made-up name each time is stopped
     * like one name used again and again.
     */
    public function testFailuresThrottledPerAddressWhateverTheName(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $token = $repository->regenerateDeployToken();

        for ($i = 0; $i < HookGate::ADDRESS_FAILURES_PER_MINUTE; $i++) {
            $this->deliver('nosuch'.$i, 'x', 'push', $this->push(), ip: '198.51.100.7')->assertNotFound();
        }
        $this->deliver('another', 'x', 'push', $this->push(), ip: '198.51.100.7')->assertStatus(429);
        $this->postRaw("/hooks/deploy/shop/{$token}", '', ip: '198.51.100.7')->assertStatus(429);
        // Other addresses are unaffected.
        $this->postRaw("/hooks/deploy/shop/{$token}", '', ip: '203.0.113.9')->assertStatus(202);

        // An IPv6 caller's /64 counts as one address.
        for ($i = 0; $i < HookGate::ADDRESS_FAILURES_PER_MINUTE; $i++) {
            $this->deliver('nosuch'.$i, 'x', 'push', $this->push(), ip: '2001:db8:1:2::'.dechex($i + 1))->assertNotFound();
        }
        $this->deliver('another', 'x', 'push', $this->push(), ip: '2001:db8:1:2:ffff::1')->assertStatus(429);
        $this->deliver('another', 'x', 'push', $this->push(), ip: '2001:db8:1:3::1')->assertNotFound();
    }

    /** Expired cache rows, which the database store never removes by itself, go daily. */
    public function testExpiredCacheRowsArePruned(): void
    {
        config(['cache.default' => 'database']);
        $now = now()->getTimestamp();
        DB::table('cache')->insert([
            ['key' => 'hooks:failures:old', 'value' => 'i:1;', 'expiration' => $now - 10],
            ['key' => 'hooks:failures:old:timer', 'value' => 'i:1;', 'expiration' => $now - 10],
            ['key' => 'hooks:failures:live', 'value' => 'i:1;', 'expiration' => $now + 50],
        ]);
        DB::table('cache_locks')->insert([
            ['key' => 'old', 'owner' => 'x', 'expiration' => $now - 10],
            ['key' => 'live', 'owner' => 'x', 'expiration' => $now + 50],
        ]);

        $this->artisan('zimam:prune-cache')->assertSuccessful();

        $this->assertSame(['hooks:failures:live'], DB::table('cache')->pluck('key')->all());
        $this->assertSame(['live'], DB::table('cache_locks')->pluck('key')->all());
        $scheduled = array_map(fn ($event): string => (string) $event->command, app(Schedule::class)->events());
        $this->assertNotEmpty(array_filter($scheduled, fn (string $command): bool => str_contains($command, 'zimam:prune-cache')));
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
