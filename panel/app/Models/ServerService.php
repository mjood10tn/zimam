<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\MorphMany;

/** An optional service on the server (MySQL now, Valkey next) and how its install went. */
class ServerService extends Model
{
    public const MYSQL = 'mysql';

    public const VALKEY = 'valkey';

    public const INSTALLING = 'installing';

    public const INSTALLED = 'installed';

    public const FAILED = 'failed';

    protected $fillable = ['name', 'status', 'version', 'last_error'];

    /** The row of a service, or null before anything was asked of it. */
    public static function named(string $name): ?self
    {
        return self::where('name', $name)->first();
    }

    public function jobs(): MorphMany
    {
        return $this->morphMany(AgentJob::class, 'related');
    }
}
