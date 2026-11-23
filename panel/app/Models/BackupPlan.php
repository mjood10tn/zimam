<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\BelongsToMany;
use Illuminate\Database\Eloquent\Relations\HasMany;
use Illuminate\Database\Eloquent\Relations\MorphMany;

/**
 * A backup plan (v0.5.0): which sites' files, which databases and whether
 * the panel's own database, a schedule, a retention and one destination.
 * The panel's editing copy of what backup.plan applies.
 *
 * @property array{keep_last: int, keep_daily?: int, keep_weekly?: int, keep_monthly?: int} $retention
 */
class BackupPlan extends Model
{
    public const SAVING = 'saving';

    public const ACTIVE = 'active';

    public const FAILED = 'failed';

    public const DELETING = 'deleting';

    /** R05-D7, mirroring the agent's BackupSchedule. */
    public const SCHEDULE_PATTERN = '/^(hourly|daily ([01][0-9]|2[0-3]):[0-5][0-9]|weekly (mon|tue|wed|thu|fri|sat|sun) ([01][0-9]|2[0-3]):[0-5][0-9])\z/';

    public const TIME_PATTERN = '/^([01][0-9]|2[0-3]):[0-5][0-9]\z/';

    public const DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];

    /** R05-D8's bound on every retention count. */
    public const MAX_KEEP = 365;

    public const DEFAULT_KEEP_LAST = 7;

    public const MAX_SITES = 50;

    public const MAX_DATABASES = 100;

    protected $fillable = ['key', 'backup_destination_id', 'schedule', 'retention', 'include_panel', 'status', 'last_error'];

    protected function casts(): array
    {
        return ['retention' => 'array', 'include_panel' => 'boolean'];
    }

    public function destination(): BelongsTo
    {
        return $this->belongsTo(BackupDestination::class, 'backup_destination_id');
    }

    public function sites(): BelongsToMany
    {
        return $this->belongsToMany(Site::class);
    }

    public function databases(): BelongsToMany
    {
        return $this->belongsToMany(Database::class);
    }

    public function runs(): HasMany
    {
        return $this->hasMany(BackupRun::class);
    }

    public function jobs(): MorphMany
    {
        return $this->morphMany(AgentJob::class, 'related');
    }

    public function hasUnfinishedJob(): bool
    {
        return $this->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists();
    }

    /** The schedule in words. */
    public function scheduleLabel(): string
    {
        $f = explode(' ', $this->schedule);

        return match ($f[0]) {
            'daily' => __('zimam.backups.schedule.daily_at', ['time' => $f[1]]),
            'weekly' => __('zimam.backups.schedule.weekly_at', ['day' => __("zimam.backups.schedule.days.{$f[1]}"), 'time' => $f[2]]),
            default => __('zimam.backups.schedule.hourly'),
        };
    }
}
