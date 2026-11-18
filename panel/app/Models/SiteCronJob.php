<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use SensitiveParameter;

/**
 * One cron job of a site (v0.4.0, R04-D5): a command that runs as the site
 * user on a schedule. The id pattern and limits mirror the agent's
 * CronJobSpec, anchored with \z; the command rules are the workers' (the
 * agent's OwnerCommand).
 */
class SiteCronJob extends Model
{
    /** Saved and sent; the outcome marks it active or failed. */
    public const APPLYING = 'applying';

    /** Left out of the submitted list: the row goes once the job succeeds. */
    public const REMOVING = 'removing';

    public const ACTIVE = 'active';

    public const FAILED = 'failed';

    public const ID_PATTERN = SiteWorker::ID_PATTERN;

    /** The id of the Laravel scheduler's script, which no job may take. */
    public const RESERVED_ID = 'scheduler';

    /** OQ3's cap: cron jobs per site. */
    public const MAX_JOBS = 50;

    protected $fillable = ['job_id', 'schedule', 'command', 'enabled', 'status'];

    protected function casts(): array
    {
        return ['enabled' => 'boolean'];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    /** The agent's OwnerCommand checks: null when $command can be saved. */
    public static function commandError(#[SensitiveParameter] string $command): ?string
    {
        return SiteWorker::commandError($command);
    }

    /**
     * The job as site.cron takes it (CronJobSpec).
     *
     * @return array{id: string, schedule: string, command: string, enabled: bool}
     */
    public function spec(): array
    {
        return ['id' => $this->job_id, 'schedule' => $this->schedule, 'command' => $this->command, 'enabled' => $this->enabled];
    }
}
