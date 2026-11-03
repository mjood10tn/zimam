<?php

namespace App\Console\Commands;

use App\Models\AgentAction;
use App\Models\AgentJob;
use App\Settings\PanelSettings;
use Illuminate\Console\Command;

/**
 * Keeps agent_jobs for the log retention period and agent_actions, the
 * panel's copy of the audit log, for the audit log's own (§7.1, D24).
 * Scheduled daily.
 */
final class PruneHistory extends Command
{
    protected $signature = 'zimam:prune-history';

    protected $description = 'Delete job history older than the log retention period, and agent actions older than the audit retention period';

    public function handle(PanelSettings $settings): int
    {
        $before = now()->subDays($settings->logRetentionDays());

        // A job still waiting or tracked stays, however old.
        $jobs = AgentJob::query()->whereIn('status', AgentJob::FINISHED)->where('created_at', '<', $before)->delete();
        $actionsBefore = now()->subDays($settings->auditRetentionDays());
        $actions = AgentAction::query()->where('created_at', '<', $actionsBefore)->delete();

        $this->line("Deleted {$jobs} jobs from before {$before->toDateTimeString()} and {$actions} agent actions from before {$actionsBefore->toDateTimeString()}.");

        return self::SUCCESS;
    }
}
