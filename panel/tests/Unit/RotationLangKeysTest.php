<?php

namespace Tests\Unit;

use Illuminate\Support\Facades\Lang;
use Tests\TestCase;

/**
 * The password flow builds its language keys from the tab's language group,
 * so a missing key would only show as the key itself. Every key it builds,
 * read from the trait and the shared partial, exists in both groups.
 */
final class RotationLangKeysTest extends TestCase
{
    /** @return list<string> */
    private static function keysBuilt(): array
    {
        $keys = [];
        $sources = [
            [app_path('Filament/Concerns/RotatesServicePassword.php'), '/zimam\.\{\$this->lang\(\)\}\.([a-z_.]+)/'],
            [resource_path('views/filament/sites/rotation.blade.php'), '/zimam\.\{\$lang\}\.([a-z_.]+)/'],
        ];
        foreach ($sources as [$file, $pattern]) {
            preg_match_all($pattern, (string) file_get_contents($file), $found);
            $keys = [...$keys, ...$found[1]];
        }

        return array_values(array_unique($keys));
    }

    public function testTheScanFindsTheKeys(): void
    {
        $this->assertContains('rotate.busy', self::keysBuilt());
        $this->assertContains('write_env.mismatch', self::keysBuilt());
        $this->assertContains('rotate.ready', self::keysBuilt());
    }

    public function testEveryKeyTheRotationFlowBuildsExists(): void
    {
        foreach (['databases', 'cache'] as $group) {
            foreach (self::keysBuilt() as $key) {
                $this->assertTrue(Lang::has("zimam.{$group}.{$key}", 'en', false), "zimam.{$group}.{$key} is missing");
            }
        }
    }
}
