<?php

namespace App\Support;

/** Sizes in binary units (KiB, MiB, GiB…), as df -h and free -h count them. */
final class Bytes
{
    public static function format(int|float $bytes, int $precision = 1): string
    {
        $units = __('zimam.units.bytes');
        $i = 0;
        while ($bytes >= 1024 && $i < count($units) - 1) {
            $bytes /= 1024;
            $i++;
        }
        $number = $i === 0 ? (string) (int) $bytes : rtrim(rtrim(number_format($bytes, $precision, '.', ''), '0'), '.');

        return "{$number} {$units[$i]}";
    }
}
