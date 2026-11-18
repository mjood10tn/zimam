<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use SensitiveParameter;

/**
 * One queue worker of a site (v0.4.0): Processes copies of a systemd unit
 * running as the site user. A laravel-queue worker takes the queue
 * options, and the agent builds its artisan line; a custom worker runs the
 * owner's command. The patterns and limits mirror the agent's WorkerSpec,
 * anchored with \z.
 *
 * @property list<string>|null $queues
 */
class SiteWorker extends Model
{
    public const LARAVEL_QUEUE = 'laravel-queue';

    public const CUSTOM = 'custom';

    public const KINDS = [self::LARAVEL_QUEUE, self::CUSTOM];

    /** Saved and sent; the outcome marks it active or failed. */
    public const APPLYING = 'applying';

    /** Left out of the submitted list: the row goes once the job succeeds. */
    public const REMOVING = 'removing';

    public const ACTIVE = 'active';

    public const FAILED = 'failed';

    public const ID_PATTERN = '/^[a-z][a-z0-9-]{0,15}\z/';

    public const CONNECTION_PATTERN = '/^[a-z0-9_-]{1,32}\z/';

    public const QUEUE_PATTERN = '/^[A-Za-z0-9_.:-]{1,64}\z/';

    /** OQ3's caps: workers per site, processes per worker, queues per worker. */
    public const MAX_WORKERS = 10;

    public const MAX_PROCESSES = 10;

    public const MAX_QUEUES = 10;

    public const MIN_MEMORY_MB = 64;

    public const MAX_MEMORY_MB = 4096;

    public const DEFAULT_MEMORY_MB = 512;

    public const DEFAULT_CONNECTION = 'default';

    /** A custom command's cap, in bytes as the agent counts them. */
    public const COMMAND_MAX_BYTES = 4096;

    /**
     * The queue options with their ranges. A max_time other than 0 is at
     * least a minute: every exit, a clean one too, counts against systemd's
     * start limit (ruling R5).
     */
    public const OPTIONS = [
        'tries' => [1, 100],
        'timeout' => [0, 86400],
        'sleep' => [0, 60],
        'max_time' => [0, 86400],
        'backoff' => [0, 3600],
    ];

    public const MIN_MAX_TIME = 60;

    protected $fillable = [
        'worker_id', 'kind', 'connection', 'queues', 'processes', 'tries', 'timeout',
        'sleep', 'max_time', 'backoff', 'memory_mb', 'command', 'status',
    ];

    protected function casts(): array
    {
        return [
            'queues' => 'array',
            'processes' => 'integer',
            'tries' => 'integer',
            'timeout' => 'integer',
            'sleep' => 'integer',
            'max_time' => 'integer',
            'backoff' => 'integer',
            'memory_mb' => 'integer',
        ];
    }

    public function site(): BelongsTo
    {
        return $this->belongsTo(Site::class);
    }

    /** The agent's OwnerCommand checks, in its order: null when $command can be saved. */
    public static function commandError(#[SensitiveParameter] string $command): ?string
    {
        return match (true) {
            $command === '' => __('zimam.workers.validation.command_empty'),
            strlen($command) > self::COMMAND_MAX_BYTES => __('zimam.workers.validation.command_size', ['max' => self::COMMAND_MAX_BYTES]),
            ! mb_check_encoding($command, 'UTF-8') => __('zimam.workers.validation.command_utf8'),
            strpbrk($command, "\0\r\n") !== false => __('zimam.workers.validation.command_line'),
            default => null,
        };
    }

    /**
     * The queue names in a comma-separated list, blanks dropped.
     *
     * @return list<string>
     */
    public static function queueList(?string $text): array
    {
        return array_values(array_filter(array_map('trim', explode(',', (string) $text)), fn (string $q): bool => $q !== ''));
    }

    /**
     * The worker as site.workers takes it (WorkerSpec): an option left
     * empty is left out, so artisan keeps its own default; a custom
     * worker takes no queue option.
     *
     * @return array<string, mixed>
     */
    public function spec(): array
    {
        $spec = ['id' => $this->worker_id, 'kind' => $this->kind, 'processes' => $this->processes, 'memory_mb' => $this->memory_mb];
        if ($this->kind === self::CUSTOM) {
            return $spec + ['command' => (string) $this->command];
        }
        // getAttribute: inside the model, $this->connection is Eloquent's database connection.
        $spec['connection'] = $this->getAttribute('connection') ?? self::DEFAULT_CONNECTION;
        if ($this->queues !== null && $this->queues !== []) {
            $spec['queues'] = array_values($this->queues);
        }
        foreach (array_keys(self::OPTIONS) as $option) {
            if ($this->{$option} !== null) {
                $spec[$option] = $this->{$option};
            }
        }

        return $spec;
    }

    /** The memory this worker may take at most, in MiB (systemd's M). */
    public function memoryCeilingMb(): int
    {
        return $this->processes * $this->memory_mb;
    }
}
