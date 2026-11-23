<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/** When an event first notified, last notified and cleared (R05-D12). */
class NotificationState extends Model
{
    protected $fillable = ['event_key', 'started_at', 'last_sent_at', 'cleared_at'];

    protected function casts(): array
    {
        return ['started_at' => 'datetime', 'last_sent_at' => 'datetime', 'cleared_at' => 'datetime'];
    }
}
