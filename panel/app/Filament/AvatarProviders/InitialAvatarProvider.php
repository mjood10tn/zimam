<?php

namespace App\Filament\AvatarProviders;

use App\Filament\Palette;
use Filament\AvatarProviders\Contracts\AvatarProvider;
use Filament\Facades\Filament;
use Illuminate\Contracts\Auth\Authenticatable;
use Illuminate\Database\Eloquent\Model;

/**
 * The user menu's avatar as an inline SVG: the first letter or digit of the
 * name, white on navy ink. Filament's default fetches it from ui-avatars.com,
 * which sends the admin's initial and IP to a third party and breaks offline.
 */
final class InitialAvatarProvider implements AvatarProvider
{
    public function get(Model|Authenticatable $record): string
    {
        // Skips leading punctuation, as Filament's default does for "[SYSTEM] Admin".
        $initial = preg_match('/[\p{L}\p{N}]/u', Filament::getNameForDefaultAvatar($record), $match)
            ? mb_strtoupper($match[0])
            : '';

        // An <img> SVG can't use the page's fonts or fetch any, so the stack is the system's.
        $svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">'
            .'<circle cx="32" cy="32" r="32" fill="'.Palette::INK[600].'"/>'
            .'<text x="32" y="32" dominant-baseline="central" text-anchor="middle" fill="#FFFFFF"'
            .' font-family="system-ui, sans-serif" font-size="28" font-weight="700">'
            .htmlspecialchars($initial, ENT_XML1 | ENT_QUOTES, 'UTF-8')
            .'</text></svg>';

        // rawurlencode, not urlencode: a browser never reads "+" in a data URI as a space.
        return 'data:image/svg+xml,'.rawurlencode($svg);
    }
}
