<?php

namespace App\Console\Commands;

use App\Models\MetricSample;
use Illuminate\Console\Command;

/** Keeps seven days of metric samples (§7.5). Scheduled daily. */
final class PruneMetrics extends Command
{
    protected $signature = 'zimam:prune-metrics';

    protected $description = 'Delete metric samples older than seven days';

    public function handle(): int
    {
        $before = now()->subDays(MetricSample::KEEP_DAYS);
        $deleted = MetricSample::query()->where('sampled_at', '<', $before)->delete();
        $this->line("Deleted {$deleted} metric samples from before {$before->toDateTimeString()}.");

        return self::SUCCESS;
    }
}
