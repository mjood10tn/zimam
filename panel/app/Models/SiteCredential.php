<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** A service user of a site; never its password. */
class SiteCredential extends Model
{
    public const MYSQL = 'mysql';

    public const VALKEY = 'valkey';

    protected $fillable = ['site_id', 'kind', 'username', 'rotated_at'];

    protected function casts(): array
    {
        return ['rotated_at' => 'immutable_datetime'];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    /** A site's MySQL and Valkey user: always s_ and the site's name. */
    public static function usernameFor(string $siteName): string
    {
        return 's_'.$siteName;
    }
}
