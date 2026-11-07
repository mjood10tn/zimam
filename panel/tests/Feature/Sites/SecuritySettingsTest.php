<?php

namespace Tests\Feature\Sites;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Filament\Resources\SiteResource\Pages\SecurityTab;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\Certificate;
use App\Models\Site;
use App\Models\SiteBasicAuthUser;
use App\Models\User;
use App\Support\IpEntry;
use Filament\Forms\Components\Repeater;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** A site's Security tab: every §9.6 setting, saved as one site.security job. */
final class SecuritySettingsTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        // Numbered repeater items, so tests can name them (Filament's testing helper).
        $undo = Repeater::fake();
        $this->beforeApplicationDestroyed($undo);
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
    }

    private function site(bool $certificate = true): Site
    {
        $site = Site::create(['name' => 'shop', 'type' => Site::PHP, 'php_version' => '8.5', 'status' => Site::ACTIVE]);
        $site->domains()->create(['hostname' => 'shop.example.com', 'is_primary' => true]);
        $site->securitySettings()->create(['force_https' => $certificate]);
        if ($certificate) {
            $site->certificate()->create(['status' => Certificate::ISSUED, 'expires_at' => now()->addDays(80)]);
            $this->agent->certify('shop');
        }

        return $site;
    }

    /** @return list<array<string, mixed>> */
    private function submitted(): array
    {
        return array_values(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit' && $c['command'] === 'site.security'), 'params'));
    }

    private function trackToTheEnd(AgentJob $job): AgentJob
    {
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
            app()->call([$track, 'handle']);
        }

        return $job;
    }

    /** Saving sends every setting at once (§9.6), whatever changed. */
    public function testFormSubmitsAllSettings(): void
    {
        $site = $this->site();

        Livewire::test(SecurityTab::class, ['site' => $site])
            ->assertFormSet([
                'force_https' => true,
                'hsts_max_age' => null,
                'headers_preset' => true,
                'ip_allow' => '',
            ])
            ->fillForm([
                'force_https' => true,
                'hsts_max_age' => '2592000',
                'hsts_include_subdomains' => true,
                'headers_preset' => false,
                'block_risky_functions' => true,
                'restrict_to_site_folder' => true,
                'ip_allow' => "192.0.2.0/24\n2001:db8::1\n",
                'ip_deny' => '203.0.113.9',
                'password_protection' => true,
                'users' => [['username' => 'alice', 'password' => 'correct horse']],
            ])
            ->call('save')
            ->assertHasNoFormErrors()
            ->assertNotified(__('zimam.security.submitted', ['name' => 'shop']));

        $params = $this->submitted();
        $this->assertCount(1, $params);
        $hash = $params[0]['password_protection']['users'][0]['password_hash'];
        $this->assertSame([
            'name' => 'shop',
            'force_https' => true,
            'hsts' => ['max_age' => 2592000, 'include_subdomains' => true],
            'headers_preset' => false,
            'block_risky_functions' => true,
            'restrict_to_site_folder' => true,
            'ip_allow' => ['192.0.2.0/24', '2001:db8::1'],
            'ip_deny' => ['203.0.113.9'],
            'password_protection' => ['enabled' => true, 'users' => [['username' => 'alice', 'password_hash' => $hash]]],
        ], $params[0]);
        $job = AgentJob::sole();
        $this->assertSame('site.security', $job->command);
        $this->assertTrue($job->related->is($site));
        $this->assertSame('[redacted]', $job->params['password_protection']['users'][0]['password_hash']);
    }

    /**
     * "Add a user" is the repeater's own action, which the browser mounts
     * with mountAction: the tab must handle actions, or every click fails
     * (Task 37's e2e found each one answered 419).
     */
    public function testAddUserActionAddsRow(): void
    {
        $site = $this->site();
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->fillForm(['password_protection' => true, 'users' => []])
            ->call('mountAction', 'add', [], ['schemaComponent' => 'form.users'])
            ->assertCount('data.users', 1);
    }

    public function testPasswordsHashedWithBcrypt12(): void
    {
        $site = $this->site();
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->fillForm(['password_protection' => true, 'users' => [['username' => 'alice', 'password' => 'correct horse']]])
            ->call('save')
            ->assertHasNoFormErrors();

        $user = SiteBasicAuthUser::sole();
        $this->assertSame('alice', $user->username);
        $this->assertStringStartsWith('$2y$12$', $user->password_hash);
        $this->assertTrue(password_verify('correct horse', $user->password_hash));
        $this->assertSame(['cost' => 12], password_get_info($user->password_hash)['options']);

        // Left empty for a user who has one, the password stays as it was.
        $this->trackToTheEnd(AgentJob::sole());
        Livewire::test(SecurityTab::class, ['site' => $site->fresh()])
            ->assertFormSet(['users.0.username' => 'alice', 'users.0.password' => null])
            ->call('save')
            ->assertHasNoFormErrors();
        $this->assertSame($user->password_hash, SiteBasicAuthUser::sole()->password_hash);
        $this->assertSame($user->password_hash, $this->submitted()[1]['password_protection']['users'][0]['password_hash']);
    }

    /** The agent only ever sees bcrypt hashes (§5.4); the plaintext isn't stored either. */
    public function testPlaintextNeverSent(): void
    {
        $site = $this->site();
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->fillForm(['password_protection' => true, 'users' => [['username' => 'alice', 'password' => 'correct horse battery']]])
            ->call('save')
            ->assertHasNoFormErrors();

        $calls = json_encode($this->agent->calls());
        $this->assertStringNotContainsString('correct horse battery', $calls);
        $this->assertMatchesRegularExpression('/^\$2y\$12\$[.\/A-Za-z0-9]{53}\z/', $this->submitted()[0]['password_protection']['users'][0]['password_hash']);
        $this->assertDatabaseMissing('site_basic_auth_users', ['password_hash' => 'correct horse battery']);
        $this->assertStringNotContainsString('correct horse battery', json_encode(AgentJob::sole()->params));
    }

    /** The panel refuses what the agent would: host bits, zones, IPv4-mapped IPv6 (§5.4, D21). */
    public function testIpValidationMirrorsAgent(): void
    {
        foreach (['10.0.0.0/8', '192.0.2.7', '192.0.2.7/32', '2001:db8::1', '2001:db8::/32', '::/0', '0.0.0.0/0', '::1.2.3.4'] as $ok) {
            $this->assertNull(IpEntry::error($ok), "{$ok} is valid");
        }
        foreach ([
            '10.0.0.1/8' => 'host bits set; use 10.0.0.0/8',
            '2001:db8::1/32' => 'use 2001:db8::/32',
            'fe80::1%eth0' => 'not a valid IP address',
            'fe80::1%eth0/64' => 'not a valid CIDR prefix',
            '::ffff:192.0.2.7' => 'not a valid IP address',
            '::ffff:192.0.2.0/120' => 'not a valid CIDR prefix',
            '010.0.0.1' => 'not a valid IP address',
            '10.0.0.0/08' => 'not a valid CIDR prefix',
            '10.0.0.0/33' => 'not a valid CIDR prefix',
            '10.0.0.0/+8' => 'not a valid CIDR prefix',
            'example.com' => 'not a valid IP address',
            '10.0.0.0/8/8' => 'not a valid CIDR prefix',
        ] as $bad => $message) {
            $this->assertStringContainsString($message, (string) IpEntry::error($bad), $bad);
        }

        $site = $this->site();
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->fillForm(['ip_allow' => "10.0.0.0/8\n10.0.0.1/8", 'ip_deny' => 'fe80::1%eth0'])
            ->call('save')
            ->assertHasFormErrors(['ip_allow', 'ip_deny']);
        $this->assertSame([], $this->submitted());

        Livewire::test(SecurityTab::class, ['site' => $site])
            ->fillForm(['ip_allow' => implode("\n", array_map(fn (int $i): string => "10.0.{$i}.0/24", range(0, 100)))])
            ->call('save')
            ->assertHasFormErrors(['ip_allow']);
        $this->assertSame([], $this->submitted());
    }

    /** Usernames as the agent takes them: no colon, no newline, unique, at most 20; a new user needs a password. */
    public function testUsernamesValidated(): void
    {
        $site = $this->site();
        foreach ([
            [['username' => 'a:b', 'password' => 'secret pass']],
            [['username' => "a\nb", 'password' => 'secret pass']],
            [['username' => 'alice', 'password' => 'one password'], ['username' => 'alice', 'password' => 'two password']],
            [['username' => 'alice', 'password' => null]],
        ] as $users) {
            Livewire::test(SecurityTab::class, ['site' => $site])
                ->fillForm(['password_protection' => true, 'users' => $users])
                ->call('save')
                ->assertHasFormErrors();
        }
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->fillForm(['password_protection' => true, 'users' => []])
            ->call('save')
            ->assertHasFormErrors(['users']);
        $this->assertSame([], $this->submitted());
    }

    /** force_https needs an issued certificate: without one the toggle is off and can't be turned on. */
    public function testForceHttpsNeedsCertificate(): void
    {
        $site = $this->site(certificate: false);
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->assertFormFieldDisabled('force_https')
            ->call('save')
            ->assertHasNoFormErrors();
        $this->assertFalse($this->submitted()[0]['force_https']);
    }

    /** The settings are stored once the agent has applied them (§7.4). */
    public function testOutcomeStoresSettings(): void
    {
        $site = $this->site();
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->fillForm([
                'hsts_max_age' => '31536000',
                'block_risky_functions' => true,
                'ip_deny' => "203.0.113.0/24\n2001:db8::/32",
                'password_protection' => true,
                'users' => [['username' => 'alice', 'password' => 'correct horse']],
            ])
            ->call('save');
        $settings = $site->securitySettings()->sole();
        $this->assertNull($settings->hsts_max_age, 'stored before the agent applied it');

        $this->trackToTheEnd(AgentJob::sole());

        $settings->refresh();
        $this->assertSame(31536000, $settings->hsts_max_age);
        $this->assertTrue($settings->block_risky_functions);
        $this->assertTrue($settings->password_protection);
        $this->assertSame(['203.0.113.0/24', '2001:db8::/32'], $settings->ip_deny);
        $this->assertSame([], $settings->ip_allow);
        $this->assertTrue($settings->force_https);
    }

    /** A failed apply leaves the stored settings as they were and shows why. */
    public function testFailedApplyKeepsSettings(): void
    {
        $site = $this->site();
        $this->agent->scriptJob('site.security', ['running', 'failed'], message: 'configuration test failed: nginx: [emerg] …');
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->fillForm(['headers_preset' => false])
            ->call('save');

        $this->trackToTheEnd(AgentJob::sole());

        $this->assertTrue($site->securitySettings()->sole()->headers_preset);
        $this->assertStringContainsString('configuration test failed', (string) $site->fresh()->last_error);
    }

    /** The tab's form is on the site page, and saving waits while another job runs on the site. */
    public function testTabShowsForm(): void
    {
        $site = $this->site();
        $this->get("/admin/sites/{$site->getRouteKey()}")
            ->assertOk()
            ->assertSee(__('zimam.security.fields.headers_preset'))
            ->assertSee(__('zimam.security.fields.ip_allow'));

        AgentJob::forceCreate(['command' => 'site.php', 'status' => AgentJob::RUNNING, 'params' => [], 'related_type' => $site->getMorphClass(), 'related_id' => $site->id]);
        Livewire::test(SecurityTab::class, ['site' => $site->fresh()])
            ->call('save')
            ->assertNotified(__('zimam.sites.busy'));
        $this->assertSame([], $this->submitted());
    }
}
