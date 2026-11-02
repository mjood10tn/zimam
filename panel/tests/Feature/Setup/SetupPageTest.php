<?php

namespace Tests\Feature\Setup;

use App\Actions\IssueSetupLink;
use App\Livewire\Setup;
use App\Models\SetupToken;
use App\Models\User;
use Filament\Facades\Filament;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Hash;
use Livewire\Livewire;
use Tests\TestCase;

/**
 * The setup page (design §4): the first visit creates the admin; with an
 * admin, the same link resets its access. Either way, 2FA comes next.
 */
final class SetupPageTest extends TestCase
{
    use RefreshDatabase;

    private const PASSWORD = 'correct horse battery staple';

    private const SECRET = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP';

    public function testCreatesAdminThenRequires2fa(): void
    {
        $token = $this->issue();

        Livewire::test(Setup::class, ['token' => $token])
            ->assertSee(__('zimam.setup.create.heading'))
            ->fillForm(['email' => 'admin@example.com', 'password' => self::PASSWORD, 'password_confirmation' => self::PASSWORD])
            ->call('save')
            ->assertHasNoFormErrors()
            ->assertRedirect(Filament::getPanel('admin')->getUrl());

        $user = User::sole();
        $this->assertSame('admin@example.com', $user->email);
        $this->assertTrue(Hash::check(self::PASSWORD, $user->password));
        $this->assertAuthenticatedAs($user);
        $this->assertNull(SetupToken::findUsable($token), 'the link works twice');

        $this->get('/admin')->assertRedirect(route('filament.admin.auth.multi-factor-authentication.set-up-required'));
    }

    /** Stored in lowercase, so sign-in doesn't depend on how it was typed (M1 review, M13). */
    public function testEmailIsStoredInLowercase(): void
    {
        Livewire::test(Setup::class, ['token' => $this->issue()])
            ->fillForm(['email' => 'Admin@Example.COM', 'password' => self::PASSWORD, 'password_confirmation' => self::PASSWORD])
            ->call('save')
            ->assertHasNoFormErrors();

        $this->assertSame('admin@example.com', User::sole()->email);
    }

    public function testPasswordRules(): void
    {
        $page = Livewire::test(Setup::class, ['token' => $this->issue()]);

        $page->fillForm(['email' => 'not an email', 'password' => 'short', 'password_confirmation' => 'short'])
            ->call('save')
            ->assertHasFormErrors(['email', 'password']);
        $page->fillForm(['email' => 'admin@example.com', 'password' => self::PASSWORD, 'password_confirmation' => self::PASSWORD.'!'])
            ->call('save')
            ->assertHasFormErrors(['password']);

        $this->assertSame(0, User::count());
        $this->assertGuest();
    }

    /**
     * One admin in v1.0 (§7.2). A page opened before the admin existed, and
     * saved after, must not create a second one, nor spend the link.
     */
    public function testSecondAdminRefused(): void
    {
        $token = $this->issue();
        $page = Livewire::test(Setup::class, ['token' => $token]);
        User::factory()->create(['email' => 'first@example.com']);

        $page->fillForm(['email' => 'second@example.com', 'password' => self::PASSWORD, 'password_confirmation' => self::PASSWORD])
            ->call('save')
            ->assertHasFormErrors(['email']);

        $this->assertSame(['first@example.com'], User::pluck('email')->all());
        $this->assertGuest();
        $this->assertNotNull(SetupToken::findUsable($token));
    }

    public function testResetClearsPasswordAnd2fa(): void
    {
        $user = $this->adminWith2fa();
        $token = $this->issue();

        Livewire::test(Setup::class, ['token' => $token])
            ->assertSee(__('zimam.setup.reset.heading'))
            ->fillForm(['email' => 'new@example.com', 'password' => self::PASSWORD, 'password_confirmation' => self::PASSWORD])
            ->call('save')
            ->assertHasNoFormErrors()
            ->assertRedirect(Filament::getPanel('admin')->getUrl());

        $user->refresh();
        $this->assertSame(1, User::count());
        $this->assertSame('new@example.com', $user->email);
        $this->assertTrue(Hash::check(self::PASSWORD, $user->password));
        $this->assertNull($user->app_authentication_secret);
        $this->assertNull($user->app_authentication_recovery_codes);
        $this->assertNotSame('old-remember-token', $user->remember_token);
        $this->assertAuthenticatedAs($user);
        $this->assertNull(SetupToken::findUsable($token));
        $this->get('/admin')->assertRedirect(route('filament.admin.auth.multi-factor-authentication.set-up-required'));
    }

    /** A link that expires while its page is open is refused when the form is saved. */
    public function testLinkExpiredBeforeSaveRefused(): void
    {
        $page = Livewire::test(Setup::class, ['token' => $this->issue()]);
        $this->travel(61)->minutes();

        $page->fillForm(['email' => 'admin@example.com', 'password' => self::PASSWORD, 'password_confirmation' => self::PASSWORD])
            ->call('save')
            ->assertStatus(404);

        $this->assertSame(0, User::count());
        $this->assertGuest();
    }

    /** The reset signs out every other session, a browser kept in by "Remember me" too (Task 3's owner decision). */
    public function testResetEndsOtherSessions(): void
    {
        $user = $this->adminWith2fa();
        foreach (['laptop', 'phone'] as $id) {
            DB::table('sessions')->insert(['id' => $id, 'user_id' => $user->id, 'payload' => '', 'last_activity' => time()]);
        }
        $remembered = $user->id.'|old-remember-token|'.$user->getAuthPassword();
        $recaller = Auth::guard('web')->getRecallerName();

        // Before the reset, that cookie alone signs the browser in.
        $this->withCookie($recaller, $remembered)->get('/admin')->assertOk();
        $this->forgetLogin();

        Livewire::test(Setup::class, ['token' => $this->issue()])
            ->fillForm(['email' => $user->email, 'password' => self::PASSWORD, 'password_confirmation' => self::PASSWORD])
            ->call('save')
            ->assertHasNoFormErrors();
        $this->forgetLogin();

        $this->assertSame(0, DB::table('sessions')->where('user_id', $user->id)->count());
        $this->withCookie($recaller, $remembered)->get('/admin')->assertRedirect(Filament::getPanel('admin')->getLoginUrl());
    }

    private function issue(): string
    {
        config(['app.url' => 'https://panel.example.com']);
        $url = app(IssueSetupLink::class)();

        return substr($url, strrpos($url, '/') + 1);
    }

    private function adminWith2fa(): User
    {
        $user = User::factory()->create(['email' => 'old@example.com', 'password' => 'the old password, long enough']);
        $user->forceFill([
            'app_authentication_secret' => self::SECRET,
            'app_authentication_recovery_codes' => [Hash::make('aaaaaaaaaa-bbbbbbbbbb')],
            'remember_token' => 'old-remember-token',
        ])->save();

        return $user->refresh();
    }

    /** A new browser: no logged-in user carried over from the last request. */
    private function forgetLogin(): void
    {
        $this->app['auth']->forgetGuards();
        $this->flushSession();
    }
}
