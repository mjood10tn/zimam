<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** One password-protection user of a site (§7.6, §9.6): a username and its bcrypt hash. */
class SiteBasicAuthUser extends Model
{
    /** Usernames as the agent takes them (§5.4): no colon or newline can reach htpasswd. */
    public const USERNAME_PATTERN = '/^[A-Za-z0-9._-]{1,32}\z/';

    /** At most this many users per site (§5.4). */
    public const MAX_USERS = 20;

    /** password_hash()'s cost: $2y$12$, inside the agent's 10–14. */
    public const BCRYPT_COST = 12;

    protected $fillable = ['username', 'password_hash'];

    protected $hidden = ['password_hash'];

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }
}
