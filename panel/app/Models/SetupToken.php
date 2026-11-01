<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/**
 * A one-time setup link (design §4). Only the token's SHA-256 is stored;
 * the link works for 60 minutes and once.
 */
class SetupToken extends Model
{
    protected $fillable = ['token_hash', 'expires_at', 'used_at'];

    protected function casts(): array
    {
        return [
            'expires_at' => 'datetime',
            'used_at' => 'datetime',
        ];
    }

    /** The unused, unexpired token whose hash matches, or null. */
    public static function findUsable(string $token): ?self
    {
        return static::query()
            ->where('token_hash', hash('sha256', $token))
            ->whereNull('used_at')
            ->where('expires_at', '>', now())
            ->first();
    }

    public function markUsed(): void
    {
        $this->forceFill(['used_at' => now()])->save();
    }
}
