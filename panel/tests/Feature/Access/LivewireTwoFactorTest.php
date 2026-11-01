<?php

namespace Tests\Feature\Access;

use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

/**
 * On an update request Livewire re-runs only the persistent middleware of
 * the page's route (Task 3, step 3). Without the 2FA check among them, a
 * page opened while 2FA was on kept running its methods after 2FA was
 * cleared with the session kept.
 */
final class LivewireTwoFactorTest extends TestCase
{
    use RefreshDatabase;

    public function testLivewireUpdateNeeds2fa(): void
    {
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
        $snapshot = $this->snapshotOf('App\Filament\Pages\Dashboard', $this->get('/admin')->assertOk()->getContent());

        $this->refresh($snapshot)->assertOk();

        $user->forceFill(['app_authentication_secret' => null, 'app_authentication_recovery_codes' => null])->save();

        $this->refresh($snapshot)->assertRedirect(route('filament.admin.auth.multi-factor-authentication.set-up-required'));
    }

    private function snapshotOf(string $component, string $html): string
    {
        preg_match_all('/wire:snapshot="([^"]*)"/', $html, $matches);
        foreach ($matches[1] as $attribute) {
            $snapshot = html_entity_decode($attribute, ENT_QUOTES);
            if ((json_decode($snapshot, true)['memo']['name'] ?? null) === $component) {
                return $snapshot;
            }
        }
        $this->fail("no snapshot of {$component} on the page");
    }

    /** What the browser sends when the page's component calls $refresh. */
    private function refresh(string $snapshot)
    {
        // Each php-fpm request starts clean. In one test process Livewire
        // remembers the route it last ran middleware for and skips it next time.
        Livewire::flushState();

        return $this->withHeader('X-Livewire', 'true')->postJson(Livewire::getUpdateUri(), [
            'components' => [['snapshot' => $snapshot, 'updates' => [], 'calls' => [['path' => '', 'method' => '$refresh', 'params' => []]]]],
        ]);
    }
}
