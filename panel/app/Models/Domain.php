<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** One of a site's domains, ASCII and lowercase (§7.6). */
class Domain extends Model
{
    /** §5.4's domain rule, anchored with \z (global rule 14); at most 253 characters besides. */
    public const PATTERN = '/^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+([a-z]{1,63}|xn--[a-z0-9-]{0,58}[a-z0-9])\z/';

    protected $fillable = ['hostname', 'is_primary'];

    protected function casts(): array
    {
        return ['is_primary' => 'boolean'];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    /**
     * What the agent gets for what the user typed: international names
     * converted with idn_to_ascii (UTS #46), then lowercased. Input that
     * can't be converted comes back lowercased as it was, for validation to
     * refuse.
     */
    public static function normalize(string $domain): string
    {
        $ascii = idn_to_ascii($domain, IDNA_DEFAULT, INTL_IDNA_VARIANT_UTS46);

        return strtolower($ascii === false ? $domain : $ascii);
    }

    public static function isValid(string $hostname): bool
    {
        return strlen($hostname) <= 253 && preg_match(self::PATTERN, $hostname) === 1;
    }
}
