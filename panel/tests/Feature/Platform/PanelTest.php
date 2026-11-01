<?php

namespace Tests\Feature\Platform;

use Tests\TestCase;

final class PanelTest extends TestCase
{
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
