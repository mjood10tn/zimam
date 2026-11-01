<?php

namespace Tests\Feature\Setup;

use App\Models\SetupToken;
use App\Settings\PanelSettings;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Artisan;
use Tests\TestCase;

/**
 * zimam:setup-link, which bootstrap and `zimam-agent panel setup-link` run
 * as zimam (design §4). Bootstrap reads the URL from the first line.
 */
final class SetupLinkCommandTest extends TestCase
{
    use RefreshDatabase;

    protected function setUp(): void
    {
        parent::setUp();
        config(['app.url' => 'http://panel.example.com:8080/']);
    }

    public function testPrintsUrlAndAllowlist(): void
    {
        $lines = $this->setupLink();

        $this->assertCount(2, $lines);
        $this->assertMatchesRegularExpression('#^https://panel\.example\.com/setup/[A-Za-z0-9_-]{43}\z#', $lines[0]);
        $this->assertNotNull(SetupToken::findUsable(substr($lines[0], strrpos($lines[0], '/') + 1)));
        $this->assertSame('Panel IP allowlist: empty', $lines[1]);

        app(PanelSettings::class)->setPanelIpAllowlist(['203.0.113.0/24', '2001:db8::/32']);
        $this->assertSame('Panel IP allowlist: 203.0.113.0/24, 2001:db8::/32', $this->setupLink()[1]);
    }

    /** The way back in when the allowlist itself locks the admin out (§4). */
    public function testClearIpAllowlist(): void
    {
        app(PanelSettings::class)->setPanelIpAllowlist(['203.0.113.0/24']);

        $this->assertSame('Panel IP allowlist: empty', $this->setupLink(['--clear-ip-allowlist' => true])[1]);
        $this->assertSame([], app(PanelSettings::class)->panelIpAllowlist());
    }

    public function testNewLinkInvalidatesOld(): void
    {
        $old = $this->token($this->setupLink()[0]);
        $new = $this->token($this->setupLink()[0]);

        $this->assertNull(SetupToken::findUsable($old));
        $this->assertNotNull(SetupToken::findUsable($new));
        $this->assertSame(1, SetupToken::count(), 'an unused token outlived its replacement');
    }

    /**
     * @param  array<string, mixed>  $options
     * @return list<string>
     */
    private function setupLink(array $options = []): array
    {
        $this->assertSame(0, Artisan::call('zimam:setup-link', $options));

        return preg_split('/\R/', rtrim(Artisan::output()));
    }

    private function token(string $url): string
    {
        return substr($url, strrpos($url, '/') + 1);
    }
}
