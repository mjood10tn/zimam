<?php

namespace Tests\Feature\Sites;

use App\Agent\AgentClient;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\FakeAgentClient;
use App\Filament\Resources\SiteResource\Pages\CreateSite;
use App\Models\AgentJob;
use App\Models\Domain;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** Creating a site (§7.3, §9.2). */
final class CreateSiteTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->agent = new FakeAgentClient;
        // 8.3 and 8.4 installed, 8.4 the default; 8.2 is installed but past its end.
        $this->agent->changeServer(function (array $status): array {
            $status['php']['installed'] = [
                ['version' => '8.2', 'fpm_active' => false, 'in_use' => false],
                ['version' => '8.3', 'fpm_active' => false, 'in_use' => false],
                ['version' => '8.4', 'fpm_active' => true, 'in_use' => true],
            ];
            $status['php']['default'] = '8.4';
            $status['php']['available'][0]['supported'] = false;

            return $status;
        });
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs(User::factory()->create());
    }

    /** @return list<array<string, mixed>> */
    private function provisions(): array
    {
        return array_values(array_map(
            fn (array $c): array => $c['params'],
            array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit' && $c['command'] === 'site.provision'),
        ));
    }

    public function testCreateSubmitsProvision(): void
    {
        Livewire::test(CreateSite::class)
            ->fillForm(['name' => 'shop', 'domain' => 'shop.example.com', 'type' => 'php', 'php_version' => '8.3'])
            ->call('create')
            ->assertHasNoFormErrors();

        $this->assertSame([['name' => 'shop', 'domain' => 'shop.example.com', 'type' => 'php', 'php_version' => '8.3']], $this->provisions());
        $site = Site::sole();
        $this->assertSame(Site::PROVISIONING, $site->status);
        $this->assertSame('php', $site->type);
        $this->assertSame('8.3', $site->php_version);
        $this->assertSame('public', $site->web_directory);
        $this->assertSame('shop.example.com', $site->primaryDomain->hostname);
        $this->assertTrue($site->primaryDomain->is_primary);
        $job = AgentJob::where('command', 'site.provision')->sole();
        $this->assertTrue($job->related->is($site));
        $this->assertSame(300, $job->runtime_max_seconds);
    }

    /** A static site's params leave php_version out altogether. */
    public function testStaticOmitsPhpVersion(): void
    {
        Livewire::test(CreateSite::class)
            ->fillForm(['name' => 'docs', 'domain' => 'docs.example.com', 'type' => 'static', 'php_version' => '8.4'])
            ->call('create')
            ->assertHasNoFormErrors();

        $this->assertSame([['name' => 'docs', 'domain' => 'docs.example.com', 'type' => 'static']], $this->provisions());
        $this->assertNull(Site::sole()->php_version);
    }

    public function testIdnConverted(): void
    {
        Livewire::test(CreateSite::class)
            ->fillForm(['name' => 'books', 'domain' => 'Bücher.Example', 'type' => 'static'])
            ->call('create')
            ->assertHasNoFormErrors();

        $this->assertSame('xn--bcher-kva.example', $this->provisions()[0]['domain']);
        $this->assertSame('xn--bcher-kva.example', Domain::sole()->hostname);
    }

    public function testNameRejectsTrailingNewline(): void
    {
        foreach (["shop\n", 'Shop', 's', 'shop-', '1shop', 'a-very-long-site-name-over-24'] as $name) {
            Livewire::test(CreateSite::class)
                ->fillForm(['name' => $name, 'domain' => 'shop.example.com', 'type' => 'static'])
                ->call('create')
                ->assertHasFormErrors(['name']);
        }
        $this->assertSame([], $this->provisions());
        $this->assertSame(0, Site::count());
    }

    public function testReservedNames(): void
    {
        foreach (['zimam', 'panel', 'default', 'acme'] as $name) {
            Livewire::test(CreateSite::class)
                ->fillForm(['name' => $name, 'domain' => 'shop.example.com', 'type' => 'static'])
                ->call('create')
                ->assertHasFormErrors(['name']);
        }
        $this->assertSame([], $this->provisions());
    }

    public function testDomainRules(): void
    {
        foreach (['localhost', 'shop.example.123', '-shop.example.com', "shop.example.com\n", '*.example.com'] as $domain) {
            Livewire::test(CreateSite::class)
                ->fillForm(['name' => 'shop', 'domain' => $domain, 'type' => 'static'])
                ->call('create')
                ->assertHasFormErrors(['domain']);
        }
        $this->assertSame([], $this->provisions());
    }

    /** Another site's name or domain is refused in the form, not by the database. */
    public function testNameAndDomainUnique(): void
    {
        $site = Site::create(['name' => 'shop', 'type' => 'static', 'status' => Site::ACTIVE]);
        $site->domains()->create(['hostname' => 'shop.example.com', 'is_primary' => true]);

        Livewire::test(CreateSite::class)
            ->fillForm(['name' => 'shop', 'domain' => 'other.example.com', 'type' => 'static'])
            ->call('create')
            ->assertHasFormErrors(['name']);
        Livewire::test(CreateSite::class)
            ->fillForm(['name' => 'blog', 'domain' => 'SHOP.example.com', 'type' => 'static'])
            ->call('create')
            ->assertHasFormErrors(['domain']);
        $this->assertSame([], $this->provisions());
    }

    /**
     * The default version is preselected, and only installed, supported
     * versions are offered, straight from system.status: php_versions may
     * be empty on a fresh server.
     */
    public function testDefaultPhpPreselected(): void
    {
        $page = Livewire::test(CreateSite::class)->assertFormSet(['php_version' => '8.4']);

        $this->assertSame(['8.3', '8.4'], array_keys($page->instance()->phpVersionOptions()));
        $page->fillForm(['name' => 'old', 'domain' => 'old.example.com', 'type' => 'php', 'php_version' => '8.2'])
            ->call('create')
            ->assertHasFormErrors(['php_version']);
    }

    public function testSecurityDefaultsRowCreated(): void
    {
        Livewire::test(CreateSite::class)
            ->fillForm(['name' => 'shop', 'domain' => 'shop.example.com', 'type' => 'static'])
            ->call('create');

        $settings = Site::sole()->securitySettings;
        $this->assertFalse($settings->force_https);
        $this->assertNull($settings->hsts_max_age);
        $this->assertFalse($settings->hsts_include_subdomains);
        $this->assertTrue($settings->headers_preset);
        $this->assertFalse($settings->block_risky_functions);
        $this->assertFalse($settings->restrict_to_site_folder);
        $this->assertSame([], $settings->ip_allow);
        $this->assertSame([], $settings->ip_deny);
        $this->assertFalse($settings->password_protection);
    }

    /** A refusal leaves no site behind and says why. */
    public function testRefusalRemovesSite(): void
    {
        $this->agent->respond('site.provision', new AgentConflict('Site blog already uses shop.example.com.'));

        Livewire::test(CreateSite::class)
            ->fillForm(['name' => 'shop', 'domain' => 'shop.example.com', 'type' => 'static'])
            ->call('create')
            ->assertNotified('Site blog already uses shop.example.com.');

        $this->assertSame(0, Site::count());
        $this->assertSame(0, Domain::count());
        $this->assertSame(0, AgentJob::count());
    }
}
