<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/**
 * One of Zimam's fail2ban jails (R06-D5), as fail2ban.config last applied
 * it. Until then the defaults below are what the form offers. The bounds
 * mirror the agent's JailMaxRetry, JailFindTime and JailBanTime.
 */
class Fail2banJail extends Model
{
    /** The jails the agent renders, in its order, with their defaults (seconds). */
    public const DEFAULTS = [
        'sshd' => ['enabled' => true, 'max_retry' => 5, 'find_time' => 600, 'ban_time' => 3600],
        'zimam-panel' => ['enabled' => true, 'max_retry' => 5, 'find_time' => 600, 'ban_time' => 3600],
        // Optional (R06-D5): off until the owner turns it on.
        'zimam-site-auth' => ['enabled' => false, 'max_retry' => 10, 'find_time' => 600, 'ban_time' => 600],
    ];

    public const MAX_RETRY = 50;

    public const FIND_TIME = [60, 86400];

    public const BAN_TIME = [60, 2592000];

    protected $fillable = ['name', 'enabled', 'max_retry', 'find_time', 'ban_time'];

    protected function casts(): array
    {
        return ['enabled' => 'boolean', 'max_retry' => 'integer', 'find_time' => 'integer', 'ban_time' => 'integer'];
    }

    /**
     * Every jail's settings, stored or default, by name in the agent's order.
     *
     * @return array<string, array{enabled: bool, max_retry: int, find_time: int, ban_time: int}>
     */
    public static function settings(): array
    {
        $stored = self::query()->get()->keyBy('name');
        $out = [];
        foreach (self::DEFAULTS as $name => $default) {
            $out[$name] = isset($stored[$name]) ? $stored[$name]->only(array_keys($default)) : $default;
        }

        return $out;
    }
}
