<?php

namespace Tests\Feature\Setup;

use App\Actions\IssueSetupLink;
use App\Models\SetupToken;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;
use Tests\TestCase;

/** The one-time setup link (design §4): 32 random bytes, stored as a hash, valid 60 minutes, used once. */
final class SetupTokenTest extends TestCase
{
    use RefreshDatabase;

    public function testTokenIs32BytesBase64Url(): void
    {
        $token = $this->issue();

        $this->assertMatchesRegularExpression('/^[A-Za-z0-9_-]{43}\z/', $token);
        $this->assertSame(32, strlen(base64_decode(strtr($token, '-_', '+/'), true)));
    }

    public function testOnlyHashStored(): void
    {
        $token = $this->issue();

        $row = (array) DB::table('setup_tokens')->sole();
        $this->assertSame(hash('sha256', $token), $row['token_hash']);
        $this->assertStringNotContainsString($token, json_encode($row));
        $this->assertNotNull(SetupToken::findUsable($token));
    }

    public function testExpiresAfter60Minutes(): void
    {
        Carbon::setTestNow('2026-11-01 10:00:00');
        $token = $this->issue();

        Carbon::setTestNow('2026-11-01 10:59:59');
        $this->assertNotNull(SetupToken::findUsable($token));
        $this->get("/setup/{$token}")->assertOk();

        Carbon::setTestNow('2026-11-01 11:00:00');
        $this->assertNull(SetupToken::findUsable($token));
        $this->get("/setup/{$token}")->assertNotFound();
    }

    public function testSingleUse(): void
    {
        $token = $this->issue();
        SetupToken::findUsable($token)->markUsed();

        $this->assertNull(SetupToken::findUsable($token));
        $this->get("/setup/{$token}")->assertNotFound();
    }

    public function testUnknownTokenNotFound(): void
    {
        $this->issue();

        $this->get('/setup/'.str_repeat('A', 43))->assertNotFound();
    }

    private function issue(): string
    {
        config(['app.url' => 'https://panel.example.com']);
        $url = app(IssueSetupLink::class)();

        return substr($url, strlen('https://panel.example.com/setup/'));
    }
}
