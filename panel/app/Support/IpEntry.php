<?php

namespace App\Support;

/**
 * The agent's rule for IP list entries, mirrored (§5.4, D21): an address or
 * a CIDR prefix in canonical form, with no host bits, no zone and no
 * IPv4-mapped IPv6 address. Patterns anchor with \z (global rule 14).
 */
final class IpEntry
{
    public const MAX_ENTRIES = 100;

    /** Null when the entry is valid; otherwise why not, as the agent words it. */
    public static function error(string $entry): ?string
    {
        if (str_contains($entry, '/')) {
            [$address, $bits] = explode('/', $entry, 2);
            $bytes = self::address($address);
            $max = $bytes === null ? 0 : strlen($bytes) * 8;
            // Go's netip.ParsePrefix takes decimal bits without a sign or leading zeros.
            if ($bytes === null || ! preg_match('/^(0|[1-9][0-9]{0,2})\z/', $bits) || (int) $bits > $max) {
                return __('zimam.security.validation.prefix');
            }
            $masked = self::mask($bytes, (int) $bits);
            if ($masked !== $bytes) {
                return __('zimam.security.validation.host_bits', ['prefix' => inet_ntop($masked).'/'.$bits]);
            }

            return null;
        }

        return self::address($entry) === null ? __('zimam.security.validation.address') : null;
    }

    /**
     * The address's bytes, or null for anything Go's netip.ParseAddr refuses
     * or the agent's rule leaves out: a zone, an IPv4-mapped IPv6 address.
     */
    private static function address(string $address): ?string
    {
        if (str_contains($address, '%') || filter_var($address, FILTER_VALIDATE_IP) === false) {
            return null;
        }
        $bytes = @inet_pton($address);
        if ($bytes === false) {
            return null;
        }
        if (strlen($bytes) === 16 && str_starts_with($bytes, str_repeat("\0", 10)."\xff\xff")) {
            return null;
        }

        return $bytes;
    }

    private static function mask(string $bytes, int $bits): string
    {
        $out = '';
        for ($i = 0; $i < strlen($bytes); $i++) {
            $keep = max(0, min(8, $bits - 8 * $i));
            $out .= chr(ord($bytes[$i]) & ((0xFF << (8 - $keep)) & 0xFF));
        }

        return $out;
    }

    /**
     * A list as typed in a textarea: one entry per line, surrounding spaces
     * and blank lines dropped.
     *
     * @return list<string>
     */
    public static function lines(?string $text): array
    {
        return array_values(array_filter(array_map('trim', preg_split('/\R/', (string) $text)), fn (string $line): bool => $line !== ''));
    }
}
