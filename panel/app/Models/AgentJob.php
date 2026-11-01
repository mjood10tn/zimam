<?php

namespace App\Models;

use App\Agent\AgentJobStatus;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\MorphTo;

/** One agent job as the panel tracks it (§7.4, §7.6). */
class AgentJob extends Model
{
    /** Retrying after `busy`, before the agent accepted it (D8). */
    public const WAITING = 'waiting';

    public const QUEUED = 'queued';

    public const RUNNING = 'running';

    public const SUCCEEDED = 'succeeded';

    public const FAILED = 'failed';

    public const CANCELLED = 'cancelled';

    public const FINISHED = [self::SUCCEEDED, self::FAILED, self::CANCELLED];

    protected $fillable = [
        'agent_job_id', 'command', 'subject', 'status', 'error_code', 'error_message', 'created_by',
        'runtime_max_seconds', 'queued_at', 'started_at', 'finished_at', 'params', 'result',
    ];

    protected function casts(): array
    {
        return [
            'created_by' => 'integer',
            'runtime_max_seconds' => 'integer',
            'queued_at' => 'immutable_datetime',
            'started_at' => 'immutable_datetime',
            'finished_at' => 'immutable_datetime',
            'params' => 'array',
            'result' => 'array',
        ];
    }

    /** The site, PHP version or certificate the outcome updates (D16). */
    public function related(): MorphTo
    {
        return $this->morphTo();
    }

    public function isFinished(): bool
    {
        return in_array($this->status, self::FINISHED, true);
    }

    /** The agent accepted the job. */
    public function markQueued(string $agentJobId): void
    {
        $this->update(['agent_job_id' => $agentJobId, 'status' => self::QUEUED, 'queued_at' => now()]);
    }

    /** The agent reports the job queued or running. */
    public function markRunning(AgentJobStatus $status): void
    {
        $this->update([
            'status' => $status->status === self::QUEUED ? self::QUEUED : self::RUNNING,
            'subject' => $status->subject ?: $this->subject,
            'started_at' => $status->startedAt ?? $this->started_at,
        ]);
    }

    /** The agent reports the job ended. */
    public function markFinished(AgentJobStatus $status): void
    {
        $failed = $status->status === self::FAILED;
        $this->update([
            'status' => $status->status,
            'subject' => $status->subject ?: $this->subject,
            'error_code' => $failed ? $status->errorCode : null,
            'error_message' => $failed ? $status->message : null,
            'result' => is_array($status->result) ? $status->result : null,
            'started_at' => $status->startedAt ?? $this->started_at,
            'finished_at' => $status->finishedAt ?? now(),
        ]);
    }

    /** The panel gave up on the job, or the agent refused it. */
    public function markFailed(string $errorCode, string $message): void
    {
        $this->update([
            'status' => self::FAILED,
            'error_code' => $errorCode,
            'error_message' => $message,
            'finished_at' => now(),
        ]);
    }
}
