<?php

namespace Tests\Feature\Deployments;

use App\Filament\Resources\SiteResource\Pages\CreateSite;
use App\Filament\Resources\SiteResource\Pages\SecurityTab;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Models\Site;
use Livewire\Livewire;

/** Laravel sites (R02-D8): a PHP site with its own type. */
final class LaravelSiteTest extends DeploymentsTestCase
{
    public function testCreateLaravelSite(): void
    {
        Livewire::test(CreateSite::class)
            ->assertSee(__('zimam.sites.types.laravel'))
            ->fillForm(['name' => 'blog', 'domain' => 'blog.example.com', 'type' => 'laravel', 'php_version' => '8.5'])
            ->assertFormFieldVisible('php_version')
            ->call('create')
            ->assertHasNoFormErrors();

        $this->assertSame([['name' => 'blog', 'domain' => 'blog.example.com', 'type' => 'laravel', 'php_version' => '8.5']], $this->submitted('site.provision'));
        $site = Site::sole();
        $this->assertSame(Site::LARAVEL, $site->type);
        $this->assertSame('8.5', $site->php_version);
        $this->assertTrue($site->runsPhp());
    }

    public function testLaravelSiteNeedsPhpVersion(): void
    {
        Livewire::test(CreateSite::class)
            ->fillForm(['name' => 'blog', 'domain' => 'blog.example.com', 'type' => 'laravel', 'php_version' => null])
            ->call('create')
            ->assertHasFormErrors(['php_version']);

        $this->assertSame([], $this->submitted('site.provision'));
    }

    /** What applies to PHP sites applies to Laravel ones: the PHP security settings and switching versions. */
    public function testLaravelSiteIsAPhpSite(): void
    {
        $site = $this->site(Site::LARAVEL, 'blog');

        $this->assertSame(['name' => 'blog', 'domain' => 'blog.example.com', 'type' => 'laravel', 'php_version' => '8.5'], $site->provisionCommand()->params());
        Livewire::test(SecurityTab::class, ['site' => $site])
            ->assertFormFieldVisible('block_risky_functions');
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->assertActionVisible('switchPhp');
        $this->assertFalse($this->site(Site::STATIC, 'docs')->runsPhp());
    }
}
