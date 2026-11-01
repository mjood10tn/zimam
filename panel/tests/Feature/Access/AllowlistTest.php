<?php

namespace Tests\Feature\Access;

use App\Actions\IssueSetupLink;
use App\Settings\PanelSettings;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

/** The optional panel IP allowlist guards every route, the setup page included (§7.2). */
final class AllowlistTest extends TestCase
{
    use RefreshDatabase;

    public function testBlocksSetupAndPanelFromOtherIp(): void
    {
        $setup = app(IssueSetupLink::class)();
        app(PanelSettings::class)->setPanelIpAllowlist(['203.0.113.0/24', '2001:db8::/32']);

        foreach (['/admin/login', parse_url($setup, PHP_URL_PATH), '/'] as $path) {
            $this->fromIp('198.51.100.7')->get($path)->assertForbidden();
            $this->fromIp('2001:db9::1')->get($path)->assertForbidden();
        }
    }

    public function testAllowsListedCidr(): void
    {
        app(PanelSettings::class)->setPanelIpAllowlist(['203.0.113.0/24', '2001:db8::/32']);

        $this->fromIp('203.0.113.9')->get('/admin/login')->assertOk();
        $this->fromIp('2001:db8::5')->get('/admin/login')->assertOk();
    }

    /** No proxy is trusted: the TCP peer is the client, whatever the headers claim (global rule 13). */
    public function testIgnoresForwardedFor(): void
    {
        app(PanelSettings::class)->setPanelIpAllowlist(['203.0.113.0/24']);

        $this->fromIp('198.51.100.7')
            ->withHeaders(['X-Forwarded-For' => '203.0.113.9', 'X-Real-IP' => '203.0.113.9', 'Forwarded' => 'for=203.0.113.9'])
            ->get('/admin/login')
            ->assertForbidden();
    }

    public function testEmptyAllowsAll(): void
    {
        $this->fromIp('198.51.100.7')->get('/admin/login')->assertOk();
    }

    /** Requests from $ip: what REMOTE_ADDR, the TCP peer, says. */
    private function fromIp(string $ip): static
    {
        return $this->withServerVariables(['REMOTE_ADDR' => $ip]);
    }
}
