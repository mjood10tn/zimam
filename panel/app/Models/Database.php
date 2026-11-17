<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\MorphMany;

/**
 * A MySQL database of one site. Its name is the site's prefix and a suffix
 * the owner chose; the agent decides which site a name belongs to.
 */
class Database extends Model
{
    public const CREATING = 'creating';

    public const ACTIVE = 'active';

    public const DROPPING = 'dropping';

    public const FAILED = 'failed';

    /** A whole database name, as the agent reads it; \z, because $ lets a trailing newline through. */
    public const NAME_PATTERN = '/^[a-z][a-z0-9_]{0,63}\z/';

    /** The part the owner types after the site's prefix. */
    public const SUFFIX_PATTERN = '/^[a-z0-9][a-z0-9_]*\z/';

    public const RESERVED_NAMES = ['mysql', 'sys', 'information_schema', 'performance_schema', 'zimam'];

    /** MySQL's limit on a database name in a grant, where each underscore counts twice. */
    public const MAX_GRANT_LENGTH = 64;

    protected $fillable = ['site_id', 'name', 'size_bytes', 'status', 'last_error'];

    protected function casts(): array
    {
        return ['size_bytes' => 'integer'];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    public function jobs(): MorphMany
    {
        return $this->morphMany(AgentJob::class, 'related');
    }

    /** What every database of a site starts with: s_, the name with hyphens as underscores, and _. */
    public static function prefixFor(string $siteName): string
    {
        return 's_'.str_replace('-', '_', $siteName).'_';
    }

    /**
     * Why a whole name is refused, as a key under zimam.databases.validation,
     * or null when it passes the shape rules. Which site a name belongs to
     * is the agent's call, not checked here.
     */
    public static function nameError(string $name): ?string
    {
        return match (true) {
            ! preg_match(self::NAME_PATTERN, $name) => 'name',
            in_array($name, self::RESERVED_NAMES, true) => 'reserved',
            strlen($name) + substr_count($name, '_') > self::MAX_GRANT_LENGTH => 'too_long',
            default => null,
        };
    }
}
