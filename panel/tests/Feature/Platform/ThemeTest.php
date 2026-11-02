<?php

namespace Tests\Feature\Platform;

use App\Filament\Palette;
use Filament\Facades\Filament;
use Filament\FontProviders\LocalFontProvider;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

/** The panel's Filament theme (DESIGN.md): its own build, its own font, navy ink for primary. */
final class ThemeTest extends TestCase
{
    // Every request reads the IP allowlist from the settings table.
    use RefreshDatabase;

    private const THEME = 'resources/css/filament/admin/theme.css';

    public function testPanelUsesTheTheme(): void
    {
        $this->assertSame(self::THEME, Filament::getPanel('admin')->getViteTheme());
    }

    /** Atkinson Hyperlegible ships with the panel, under its licence; no page asks a font CDN for it. */
    public function testFontIsBundled(): void
    {
        $panel = Filament::getPanel('admin');
        $this->assertSame('Atkinson Hyperlegible', $panel->getFontFamily());
        $this->assertSame(LocalFontProvider::class, $panel->getFontProvider());

        $css = file_get_contents(base_path(self::THEME));
        preg_match_all("/url\\('([^']+\\.woff2)'\\)/", $css, $urls);
        $this->assertCount(4, $urls[1]);
        foreach ($urls[1] as $url) {
            $this->assertFileExists(dirname(base_path(self::THEME)).'/'.$url);
        }
        $this->assertFileExists(resource_path('fonts/atkinson-hyperlegible/OFL.txt'));

        $this->get('/admin/login')->assertOk()
            ->assertDontSee('fonts.bunny.net')
            ->assertDontSee('fonts.googleapis.com');
    }

    /** The Emerald Is the Brand rule: primary drives navigation, focus and links, so it is navy ink. */
    public function testPrimaryIsInk(): void
    {
        $colors = Filament::getPanel('admin')->getColors();
        $this->assertSame(Palette::INK, $colors['primary']);
        $this->assertSame(Palette::INK, $colors['success']);
    }
}
