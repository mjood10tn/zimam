<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * One deploy of a site (R02-D5). Its status follows its site.deploy job's
 * through every change, from waiting after busy to the outcome; its log is
 * the job's.
 */
class Deployment extends Model
{
    public const MANUAL = 'manual';

    public const WEBHOOK = 'webhook';

    public const URL = 'url';

    public const TRIGGERS = [self::MANUAL, self::WEBHOOK, self::URL];

    public const WAITING = AgentJob::WAITING;

    public const QUEUED = AgentJob::QUEUED;

    public const RUNNING = AgentJob::RUNNING;

    public const SUCCEEDED = AgentJob::SUCCEEDED;

    public const FAILED = AgentJob::FAILED;

    public const CANCELLED = AgentJob::CANCELLED;

    protected $fillable = [
        'site_id', 'agent_job_id', 'trigger', 'commit', 'release', 'status', 'error_message',
        'created_by', 'started_at', 'finished_at',
    ];

    protected function casts(): array
    {
        return [
            'created_by' => 'integer',
            'started_at' => 'immutable_datetime',
            'finished_at' => 'immutable_datetime',
        ];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    public function agentJob(): BelongsTo
    {
        return $this->belongsTo(AgentJob::class);
    }

    public function creator(): BelongsTo
    {
        return $this->belongsTo(User::class, 'created_by');
    }

    public function isFinished(): bool
    {
        return in_array($this->status, AgentJob::FINISHED, true);
    }

    /** Brings the deployments of $job up to its state; a succeeded one gets its release and commit. */
    public static function follow(AgentJob $job): void
    {
        foreach (self::query()->where('agent_job_id', $job->id)->get() as $deployment) {
            $result = $job->status === AgentJob::SUCCEEDED && is_array($job->result) ? $job->result : [];
            $deployment->update([
                'status' => $job->status,
                'started_at' => $job->started_at,
                'finished_at' => $job->finished_at,
                'error_message' => $job->status === AgentJob::FAILED ? $job->error_message : null,
                'release' => is_string($result['release'] ?? null) ? $result['release'] : $deployment->release,
                'commit' => is_string($result['commit'] ?? null) ? $result['commit'] : $deployment->commit,
            ]);
        }
    }
}
