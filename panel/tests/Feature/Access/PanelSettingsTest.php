<?php

namespace Tests\Feature\Access;

use App\Settings\PanelSettings;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\DB;
use Tests\TestCase;

/** The panel's settings rows (§7.6), read through typed getters with the §4 and §11.4 defaults. */
final class PanelSettingsTest extends TestCase
{
    use RefreshDatabase;

    public function testDefaults(): void
    {
        $settings = app(PanelSettings::class);

        $this->assertSame(14, $settings->logRetentionDays());
        $this->assertSame(5 * 1024 * 1024 * 1024, $settings->siteLogCapBytes());
        $this->assertSame([], $settings->panelIpAllowlist());
        $this->assertSame(0, DB::table('settings')->count(), 'reading a default wrote a row');
    }

    public function testStoresValuesAsJson(): void
    {
        app(PanelSettings::class)->setPanelIpAllowlist(['203.0.113.0/24']);

        $this->assertSame(['203.0.113.0/24'], app(PanelSettings::class)->panelIpAllowlist());
        $this->assertSame('["203.0.113.0\/24"]', DB::table('settings')->where('key', 'panel_ip_allowlist')->value('value'));
    }
}
