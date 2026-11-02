<?php

namespace Tests\Feature\Platform;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Filament\AvatarProviders\InitialAvatarProvider;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use PHPUnit\Framework\Attributes\DataProvider;
use Tests\TestCase;

/** The user menu's avatar is drawn by the panel, so no page sends the admin's initial and IP to ui-avatars.com. */
final class AvatarTest extends TestCase
{
    use RefreshDatabase;

    private const PREFIX = 'data:image/svg+xml,';

    public function testPageLoadsNoAvatarFromAnotherHost(): void
    {
        $this->app->instance(AgentClient::class, new FakeAgentClient);
        $user = User::factory()->create(['name' => 'Admin']);
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);

        $this->get('/admin')->assertOk()
            ->assertDontSee('ui-avatars.com')
            ->assertDontSee('src="https://', false)
            ->assertSee('src="'.self::PREFIX, false);
    }

    /** @return array<string, array{string, string}> */
    public static function names(): array
    {
        return [
            'latin' => ['admin', 'A'],
            'arabic' => ['عبدالمجيد', 'ع'],
            'leading punctuation' => ['[SYSTEM] Admin', 'S'],
            'no letters' => ['', ''],
        ];
    }

    /** A white initial on DESIGN.md's navy ink, as an SVG data URI that decodes the way a browser reads it. */
    #[DataProvider('names')]
    public function testDrawsInitialOnInk(string $name, string $initial): void
    {
        $uri = app(InitialAvatarProvider::class)->get((new User)->forceFill(['name' => $name]));

        $this->assertStringStartsWith(self::PREFIX, $uri);
        // Browsers percent-decode a data URI but never read "+" as a space.
        $svg = simplexml_load_string(rawurldecode(substr($uri, strlen(self::PREFIX))));
        $this->assertNotFalse($svg, 'not well-formed SVG');
        $this->assertSame('#0B2545', (string) $svg->circle['fill']);
        $this->assertSame('#FFFFFF', (string) $svg->text['fill']);
        $this->assertSame($initial, (string) $svg->text);
    }
}
