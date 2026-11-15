<?php

namespace App\Support;

use App\Settings\PanelSettings;
use Carbon\CarbonInterface;

/**
 * The zone the panel shows times in (the display_timezone setting). Times
 * are stored and handled in UTC; only what a person reads is converted.
 */
final class DisplayTime
{
    public static function zone(): string
    {
        return app(PanelSettings::class)->displayTimezone();
    }

    public static function format(?CarbonInterface $at, string $format): string
    {
        return $at === null ? '' : $at->copy()->setTimezone(self::zone())->format($format);
    }
}
