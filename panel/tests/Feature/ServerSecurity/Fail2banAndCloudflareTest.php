<?php

namespace Tests\Feature\ServerSecurity;

use App\Filament\Pages\ServerSecurity\CloudflareForm;
use App\Filament\Pages\ServerSecurity\Fail2banForm;
use App\Livewire\GuardedChangeModal;
use App\Models\AgentJob;
use App\Models\Fail2banJail;
use App\Models\GuardedChange;
use App\Settings\PanelSettings;
use Livewire\Livewire;
use Tests\Feature\Deployments\DeploymentsTestCase;

/** The Server security page's fail2ban and Cloudflare sections (R06-D5, D7, D9, OQ4, OQ5). */
final class Fail2banAndCloudflareTest extends DeploymentsTestCase
{
    /** @return array<string, mixed> the form as the owner fills it: every jail on, times in minutes */
    private static function jails(): array
    {
        return [
            'sshd' => ['enabled' => true, 'max_retry' => 5, 'find_minutes' => 10, 'ban_minutes' => 60],
            'zimam-panel' => ['enabled' => true, 'max_retry' => 5, 'find_minutes' => 10, 'ban_minutes' => 60],
            'zimam-site-auth' => ['enabled' => false, 'max_retry' => 10, 'find_minutes' => 10, 'ban_minutes' => 10],
        ];
    }

    public function testJailSettingsSubmit(): void
    {
        Livewire::test(Fail2banForm::class)
            ->fillForm(['jails' => self::jails(), 'ignore' => "203.0.113.0/24\n2001:db8::7"])
            ->callAction('save', ['current_password' => self::PASSWORD])
            ->assertNotified(__('zimam.server_security.fail2ban.submitted'));

        $this->assertSame([[
            'jails' => [
                ['name' => 'sshd', 'enabled' => true, 'max_retry' => 5, 'find_time' => 600, 'ban_time' => 3600],
                ['name' => 'zimam-panel', 'enabled' => true, 'max_retry' => 5, 'find_time' => 600, 'ban_time' => 3600],
                ['name' => 'zimam-site-auth', 'enabled' => false, 'max_retry' => 10, 'find_time' => 600, 'ban_time' => 600],
            ],
            'ignore' => ['203.0.113.0/24', '2001:db8::7'],
        ]], $this->submitted('fail2ban.config'));

        // The panel's copy follows once the job has applied them.
        $this->trackToTheEnd(AgentJob::sole());
        $this->assertFalse(Fail2banJail::where('name', 'zimam-site-auth')->sole()->enabled);
        $this->assertSame(['203.0.113.0/24', '2001:db8::7'], app(PanelSettings::class)->fail2banIgnore());
    }

    public function testBadIgnoreEntryRefused(): void
    {
        Livewire::test(Fail2banForm::class)
            ->fillForm(['ignore' => '10.0.0.1/8'])
            ->callAction('save', ['current_password' => self::PASSWORD])
            ->assertHasErrors(['data.ignore']);
        $this->assertSame([], $this->submitted('fail2ban.config'));
    }

    public function testIgnorePrefilledWithCurrentAddress(): void
    {
        $form = Livewire::test(Fail2banForm::class);
        $this->assertStringContainsString(request()->ip(), $form->get('data.ignore'));

        // Once saved, the owner's list is what shows.
        app(PanelSettings::class)->setFail2banIgnore(['203.0.113.0/24']);
        Livewire::test(Fail2banForm::class)->assertSet('data.ignore', '203.0.113.0/24');
    }

    public function testBannedListedAndUnban(): void
    {
        $this->agent->fail2banBan('sshd', '192.0.2.44');

        Livewire::test(Fail2banForm::class)
            ->assertSee('192.0.2.44')
            ->callAction('unban', [], ['jail' => 'sshd', 'address' => '192.0.2.44'])
            ->assertNotified(__('zimam.server_security.fail2ban.unban.submitted', ['address' => '192.0.2.44']));

        $this->assertSame([['jail' => 'sshd', 'address' => '192.0.2.44']], $this->submitted('fail2ban.unban'));
    }

    public function testCloudflareModeSubmits(): void
    {
        Livewire::test(CloudflareForm::class)
            ->fillForm(['enabled' => true, 'restrict_origin' => false])
            ->callAction('save', ['current_password' => self::PASSWORD])
            ->assertNotified(__('zimam.server_security.cloudflare.submitted'));

        $this->assertSame([['enabled' => true, 'restrict_origin' => false]], $this->submitted('cloudflare.mode'));
        $this->trackToTheEnd(AgentJob::sole());
        // Without the origin restriction there is nothing to keep.
        $this->assertSame(0, GuardedChange::count());
        $this->assertTrue(app(PanelSettings::class)->cloudflare()['enabled']);
    }

    public function testRestrictOriginWarnsWithSites(): void
    {
        $this->site(name: 'shop');
        $this->site(name: 'blog');

        Livewire::test(CloudflareForm::class)
            ->fillForm(['enabled' => true, 'restrict_origin' => true])
            ->mountAction('save')
            ->assertMountedActionModalSee('blog.example.com')
            ->assertMountedActionModalSee('shop.example.com')
            ->assertMountedActionModalSee(__('zimam.server_security.cloudflare.restrict_warning'));

        Livewire::test(CloudflareForm::class)
            ->fillForm(['enabled' => true, 'restrict_origin' => false])
            ->mountAction('save')
            ->assertMountedActionModalDontSee(__('zimam.server_security.cloudflare.restrict_warning'));
    }

    public function testRestrictOriginIsKeptFromTheModal(): void
    {
        Livewire::test(CloudflareForm::class)
            ->fillForm(['enabled' => true, 'restrict_origin' => true])
            ->callAction('save', ['current_password' => self::PASSWORD]);
        $this->trackToTheEnd(AgentJob::sole());

        $change = GuardedChange::sole();
        $this->assertSame(GuardedChange::CLOUDFLARE, $change->kind);
        // In force but not kept: the panel still says unrestricted.
        $this->assertFalse(app(PanelSettings::class)->cloudflare()['restrict_origin']);

        Livewire::test(GuardedChangeModal::class)
            ->assertSee(__('zimam.server_security.change.heading.cloudflare'))
            ->callMountedAction();

        $this->assertTrue(app(PanelSettings::class)->cloudflare()['restrict_origin']);
    }

    public function testRestrictNeedsCloudflareMode(): void
    {
        Livewire::test(CloudflareForm::class)
            ->fillForm(['enabled' => false, 'restrict_origin' => true])
            ->callAction('save', ['current_password' => self::PASSWORD]);

        $this->assertSame([['enabled' => false, 'restrict_origin' => false]], $this->submitted('cloudflare.mode'));
    }

    public function testFail2banExplainsCloudflareMode(): void
    {
        Livewire::test(Fail2banForm::class)->assertDontSee(__('zimam.server_security.fail2ban.cloudflare_note'));
        app(PanelSettings::class)->setCloudflare(true, false);
        Livewire::test(Fail2banForm::class)->assertSee(__('zimam.server_security.fail2ban.cloudflare_note'));
    }
}
