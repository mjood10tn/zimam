<?php

namespace Tests\Feature\Access;

use App\Actions\IssueSetupLink;
use App\Filament\Pages\Auth\Login;
use App\Models\User;
use App\Support\AuthFailureLog;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use PragmaRX\Google2FA\Google2FA;
use Tests\TestCase;

/** A failed password, 2FA code or setup link writes one line for fail2ban's zimam-panel jail (R06-D6). */
final class AuthFailureLogTest extends TestCase
{
    use RefreshDatabase;

    private const SECRET = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP';

    /** @var list<string> */
    private array $lines = [];

    protected function setUp(): void
    {
        parent::setUp();
        $this->app->instance(AuthFailureLog::class, new AuthFailureLog(function (string $line): void {
            $this->lines[] = $line;
        }));
        User::factory()->create(['email' => 'admin@example.com', 'password' => 'the right password']);
    }

    public function testFailedLoginWritesSyslogLine(): void
    {
        Livewire::test(Login::class)
            ->fillForm(['email' => 'admin@example.com', 'password' => 'wrong'])
            ->call('authenticate');

        $this->assertSame(['auth failure from 127.0.0.1 kind=password'], $this->lines);
    }

    public function testLineHasNoEmail(): void
    {
        Livewire::test(Login::class)
            ->fillForm(['email' => 'nobody@example.com', 'password' => 'wrong'])
            ->call('authenticate');

        $this->assertCount(1, $this->lines);
        $this->assertStringNotContainsString('example.com', $this->lines[0]);
    }

    public function testRightPasswordWritesNothing(): void
    {
        Livewire::test(Login::class)
            ->fillForm(['email' => 'admin@example.com', 'password' => 'the right password'])
            ->call('authenticate');

        $this->assertSame([], $this->lines);
    }

    public function testFailedTotpWritesSyslogLine(): void
    {
        User::query()->firstOrFail()->forceFill(['app_authentication_secret' => self::SECRET])->save();
        $g2fa = app(Google2FA::class);
        $old = $g2fa->oathTotp(self::SECRET, $g2fa->getTimestamp() - 3);

        $page = Livewire::test(Login::class)
            ->fillForm(['email' => 'admin@example.com', 'password' => 'the right password'])
            ->call('authenticate')
            ->assertHasNoFormErrors();
        $this->assertSame([], $this->lines, 'the right password was logged');

        $page->set('data.multiFactor.app.code', $old)->call('authenticate')->assertHasFormErrors();
        $this->assertSame(['auth failure from 127.0.0.1 kind=totp'], $this->lines);

        // The right code signs in and logs nothing more.
        $page->set('data.multiFactor.app.code', $g2fa->getCurrentOtp(self::SECRET))->call('authenticate')->assertHasNoFormErrors();
        $this->assertAuthenticated();
        $this->assertCount(1, $this->lines);
    }

    public function testRefusedSetupLinkWritesLine(): void
    {
        $this->get('/setup/not-a-token')->assertNotFound();

        $this->assertSame(['auth failure from 127.0.0.1 kind=setup'], $this->lines);
    }

    public function testUsableSetupLinkWritesNothing(): void
    {
        $this->get(parse_url(app(IssueSetupLink::class)(), PHP_URL_PATH))->assertOk();

        $this->assertSame([], $this->lines);
    }

    /** Only a parsed address and a known kind reach the line. */
    public function testRefusesWhatIsNotAnAddressOrKind(): void
    {
        $log = app(AuthFailureLog::class);
        $log->record('password', '203.0.113.9 kind=setup');
        $log->record('password', null);
        $log->record('other', '203.0.113.9');
        $log->record('totp', '2001:DB8:0::1');

        $this->assertSame(['auth failure from 2001:db8::1 kind=totp'], $this->lines);
    }
}
