<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;

/**
 * One agent call the panel logs (D9, §7.6): a submit, a job.cancel or a
 * rejected request. Rows are written once and never updated.
 */
class AgentAction extends Model
{
    public const UPDATED_AT = null;

    protected $fillable = ['command', 'actor_id', 'params', 'outcome', 'duration_ms'];

    protected function casts(): array
    {
        return [
            'actor_id' => 'integer',
            'params' => 'array',
            'duration_ms' => 'integer',
        ];
    }
}
