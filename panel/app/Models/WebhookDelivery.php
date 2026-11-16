<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/** A GitHub delivery id already handled, kept 24 hours so a redelivery deploys once (R02-D11). */
class WebhookDelivery extends Model
{
    /** How long a delivery id is remembered. */
    public const KEEP_HOURS = 24;

    public $timestamps = false;

    protected $fillable = ['delivery_id', 'site_id', 'received_at'];

    protected function casts(): array
    {
        return ['received_at' => 'immutable_datetime'];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }
}
