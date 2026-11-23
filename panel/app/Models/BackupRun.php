<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * One run of a plan, copied from the agent's run record (R05-D9). A
 * succeeded run is a complete backup: its manifest lists every part.
 *
 * @property list<array{part: string, bytes: int, sha256: string}> $parts
 */
class BackupRun extends Model
{
    public const RUNNING = 'running';

    public const SUCCEEDED = 'succeeded';

    public const FAILED = 'failed';

    public const TIMER = 'timer';

    public const PANEL = 'panel';

    /** R05-D6: the run's UTC start. */
    public const ID_PATTERN = '/^[0-9]{8}T[0-9]{6}Z\z/';

    public const ID_FORMAT = 'Ymd\THis\Z';

    /** The panel's own database part, which the agent doesn't restore (OQ8). */
    public const PANEL_PART = 'panel';

    /** The tail of a run's log the panel shows. */
    public const LOG_BYTES = 64 * 1024;

    protected $fillable = ['backup_plan_id', 'backup_id', 'trigger', 'status', 'size_bytes', 'parts', 'error', 'started_at', 'finished_at', 'notified_at'];

    protected function casts(): array
    {
        return [
            'parts' => 'array',
            'size_bytes' => 'integer',
            'started_at' => 'datetime',
            'finished_at' => 'datetime',
            'notified_at' => 'datetime',
        ];
    }

    public function plan(): BelongsTo
    {
        return $this->belongsTo(BackupPlan::class, 'backup_plan_id');
    }

    /**
     * The parts the agent can restore: databases and site files.
     *
     * @return list<string>
     */
    public function restorableParts(): array
    {
        return array_values(array_filter(array_column($this->parts, 'part'), fn (string $p): bool => $p !== self::PANEL_PART));
    }

    /**
     * The end of the run's log, /var/log/zimam/backups/<plan>/<id>.log,
     * which the panel's user reads through the zimam-agent group like job
     * logs; null when it is missing (pruned, or under the fake agent). The
     * path is built only from validated ids.
     */
    public function logTail(): ?string
    {
        $plan = $this->plan->key;
        if (! preg_match(BackupDestination::KEY_PATTERN, $plan) || ! preg_match(self::ID_PATTERN, $this->backup_id)) {
            return null;
        }
        $path = config('zimam.backup_logs')."/{$plan}/{$this->backup_id}.log";
        $size = @filesize($path);
        if ($size === false) {
            return null;
        }
        $text = @file_get_contents($path, false, null, max(0, $size - self::LOG_BYTES));

        return $text === false ? null : $text;
    }
}
