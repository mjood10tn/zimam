<?php

namespace Tests\Feature\Access;

use App\Filament\Pages\Auth\Login;
use App\Models\User;
use App\Support\AuthFailureLog;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

/** A failed sign-in writes one line for fail2ban's zimam-panel jail (R06-D6). */
final class AuthFailureLogTest extends TestCase
{
    use RefreshDatabase;

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
