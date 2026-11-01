<?php

namespace App\Console\Commands;

use App\Models\AgentAction;
use App\Models\AgentJob;
use App\Settings\PanelSettings;
use Illuminate\Console\Command;

/** Keeps agent_jobs and agent_actions for the log retention period (§7.1). Scheduled daily. */
final class PruneHistory extends Command
{
    protected $signature = 'zimam:prune-history';

    protected $description = 'Delete job and agent action history older than the log retention period';

    public function handle(PanelSettings $settings): int
    {
        $before = now()->subDays($settings->logRetentionDays());

        // A job still waiting or tracked stays, however old.
        $jobs = AgentJob::query()->whereIn('status', AgentJob::FINISHED)->where('created_at', '<', $before)->delete();
        $actions = AgentAction::query()->where('created_at', '<', $before)->delete();

        $this->line("Deleted {$jobs} jobs and {$actions} agent actions from before {$before->toDateTimeString()}.");

        return self::SUCCESS;
    }
}
