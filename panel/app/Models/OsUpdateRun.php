<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\MorphMany;

/** One "Install updates now" and what it changed (R06-D13). */
class OsUpdateRun extends Model
{
    public const RUNNING = 'running';

    public const SUCCEEDED = 'succeeded';

    public const FAILED = 'failed';

    public const SCOPES = ['security', 'all'];

    protected $fillable = ['agent_job_id', 'scope', 'status', 'upgraded', 'restarted', 'reboot_required', 'started_at', 'finished_at'];

    protected function casts(): array
    {
        return ['upgraded' => 'array', 'restarted' => 'array', 'reboot_required' => 'boolean', 'started_at' => 'immutable_datetime', 'finished_at' => 'immutable_datetime'];
    }

    public function agentJob(): BelongsTo
    {
        return $this->belongsTo(AgentJob::class);
    }

    public function jobs(): MorphMany
    {
        return $this->morphMany(AgentJob::class, 'related');
    }
}
