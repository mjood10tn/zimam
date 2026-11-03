<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\MorphMany;

/** A site's Let's Encrypt certificate (§7.6, §10). The agent renews it; the panel learns new expiry dates through security.check. */
class Certificate extends Model
{
    public const ISSUING = 'issuing';

    public const ISSUED = 'issued';

    public const FAILED = 'failed';

    /** How close to its end a certificate is a warning (§9.7). */
    public const WARN_DAYS = 14;

    protected $fillable = ['status', 'expires_at', 'last_error'];

    protected function casts(): array
    {
        return ['expires_at' => 'immutable_datetime'];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    /** The cert.issue jobs whose outcome updates this certificate. */
    public function jobs(): MorphMany
    {
        return $this->morphMany(AgentJob::class, 'related');
    }
}
