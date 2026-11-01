<?php

namespace Tests\Feature\Access;

use App\Filament\Pages\Auth\Login;
use App\Models\User;
use Filament\Facades\Filament;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\RateLimiter;
use Livewire\Livewire;
use PragmaRX\Google2FA\Google2FA;
use Tests\TestCase;

/** Login is limited to 5 attempts per minute per email and IP address (§7.2). */
final class LoginThrottleTest extends TestCase
{
    use RefreshDatabase;

    private const SECRET = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP';

    protected function setUp(): void
    {
        parent::setUp();
        $user = User::factory()->create(['email' => 'admin@example.com', 'password' => 'the right password']);
        $user->forceFill(['app_authentication_secret' => self::SECRET])->save();
    }

    public function testSixthAttemptBlocked(): void
    {
        $page = Livewire::test(Login::class);
        for ($i = 1; $i <= 5; $i++) {
            $this->attempt($page, 'admin@example.com', 'wrong')->assertHasFormErrors(['email'])->assertNotNotified();
        }

        $key = 'livewire-rate-limiter:'.sha1(Login::class.'|authenticate|admin@example.com|127.0.0.1');
        $this->attempt($page, 'admin@example.com', 'wrong')->assertNotified();
        // The right password is throttled too: the limit is on attempts, not failures.
        $this->attempt($page, 'admin@example.com', 'the right password')->assertNotified();
        $this->assertGuest();
        $this->assertSame(5, RateLimiter::attempts($key), 'a throttled attempt was let through to the password check');
    }

    /** Filament keys its limiter on the IP alone; Zimam's adds the email, in any case. */
    public function testKeyIsEmailAndIp(): void
    {
        $page = Livewire::test(Login::class);
        for ($i = 1; $i <= 5; $i++) {
            $this->attempt($page, 'admin@example.com', 'wrong');
        }

        $this->attempt(Livewire::test(Login::class), 'someone@example.com', 'wrong')->assertHasFormErrors(['email'])->assertNotNotified();
        $this->attempt(Livewire::test(Login::class), ' ADMIN@example.com', 'wrong')->assertNotified();
    }

    /** The panel serves this page, not Filament's own, whose limit is per IP alone. */
    public function testPanelUsesThisLoginPage(): void
    {
        $this->get(Filament::getPanel('admin')->getLoginUrl())->assertOk()->assertSeeLivewire(Login::class);
    }

    /**
     * The 2FA step calls authenticate() a second time (Task 3, step 4), so
     * a full login uses two of the five attempts.
     */
    public function testTwoFactorLoginUsesTwoAttempts(): void
    {
        $page = $this->attempt(Livewire::test(Login::class), 'admin@example.com', 'the right password')->assertHasNoFormErrors();
        $this->assertGuest();

        $page->set('data.multiFactor.app.code', app(Google2FA::class)->getCurrentOtp(self::SECRET))
            ->call('authenticate')
            ->assertHasNoFormErrors();

        $this->assertAuthenticated();
        $key = 'livewire-rate-limiter:'.sha1(Login::class.'|authenticate|admin@example.com|127.0.0.1');
        $this->assertSame(2, RateLimiter::attempts($key));
    }

    private function attempt($page, string $email, string $password)
    {
        return $page->fillForm(['email' => $email, 'password' => $password])->call('authenticate');
    }
}
