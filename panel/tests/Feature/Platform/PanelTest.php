<?php

namespace Tests\Feature\Platform;

use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

final class PanelTest extends TestCase
{
    // Every request reads the IP allowlist from the settings table.
    use RefreshDatabase;

    /** The brand comes from lang/en (D3), not from APP_NAME, Filament's default. */
    public function testLoginPageRenders(): void
    {
        config(['app.name' => 'Laravel']);

        $this->get('/admin/login')
            ->assertOk()
            ->assertSee(__('zimam.brand'));
    }

    /** The panel lives under /admin; its domain's root leads there. */
    public function testRootRedirectsToPanel(): void
    {
        $this->get('/')->assertRedirect('/admin');
    }
}
