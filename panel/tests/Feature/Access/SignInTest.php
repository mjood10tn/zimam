<?php

namespace Tests\Feature\Access;

use App\Filament\Pages\Auth\Login;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use PragmaRX\Google2FA\Google2FA;
use Tests\TestCase;

/** Sign-in details the Milestone 1 review found (M8, M13). */
final class SignInTest extends TestCase
{
    use RefreshDatabase;

    private const SECRET = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP';

    protected function setUp(): void
    {
        parent::setUp();
        $user = User::factory()->create(['email' => 'admin@example.com', 'password' => 'the right password']);
        $user->forceFill(['app_authentication_secret' => self::SECRET])->save();
    }

    /** The email matches whatever case it's typed in (M13). */
    public function testEmailInAnyCase(): void
    {
        $page = Livewire::test(Login::class)
            ->fillForm(['email' => 'Admin@EXAMPLE.com', 'password' => 'the right password'])
            ->call('authenticate')
            ->assertHasNoFormErrors();

        $page->set('data.multiFactor.app.code', app(Google2FA::class)->getCurrentOtp(self::SECRET))
            ->call('authenticate')
            ->assertHasNoFormErrors();
        $this->assertAuthenticated();
    }

    /**
     * A code is valid for its own 30 seconds and one period either side,
     * not Filament's default of eight, ±4 minutes (M8). A code three periods
     * old stays refused even if a period passes during the test.
     */
    public function testCodeWindowIsOnePeriod(): void
    {
        $g2fa = app(Google2FA::class);
        $old = $g2fa->oathTotp(self::SECRET, $g2fa->getTimestamp() - 3);

        Livewire::test(Login::class)
            ->fillForm(['email' => 'admin@example.com', 'password' => 'the right password'])
            ->call('authenticate')
            ->assertHasNoFormErrors()
            ->set('data.multiFactor.app.code', $old)
            ->call('authenticate')
            ->assertHasFormErrors();
        $this->assertGuest();
    }
}
