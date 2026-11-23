<?php

namespace App\Console\Commands;

use App\Models\AgentAction;
use App\Models\AgentJob;
use App\Models\BackupRun;
use App\Models\NotificationState;
use App\Models\WebhookDelivery;
use App\Settings\PanelSettings;
use Illuminate\Console\Command;

/**
 * Keeps agent_jobs for the log retention period and agent_actions, the
 * panel's copy of the audit log, for the audit log's own (§7.1, D24). A
 * deployment goes with its job. GitHub delivery ids are kept 24 hours
 * (R02-D11). Scheduled daily.
 */
final class PruneHistory extends Command
{
    protected $signature = 'zimam:prune-history';

    protected $description = 'Delete job history older than the log retention period, agent actions older than the audit retention period, and webhook deliveries older than a day';

    public function handle(PanelSettings $settings): int
    {
        $before = now()->subDays($settings->logRetentionDays());

        // A job still waiting or tracked stays, however old. Its deployment
        // row, if it has one, goes with it (cascade).
        $jobs = AgentJob::query()->whereIn('status', AgentJob::FINISHED)->where('created_at', '<', $before)->delete();
        $actionsBefore = now()->subDays($settings->auditRetentionDays());
        $actions = AgentAction::query()->where('created_at', '<', $actionsBefore)->delete();
        $deliveries = WebhookDelivery::query()->where('received_at', '<', now()->subHours(WebhookDelivery::KEEP_HOURS))->delete();
        // Finished backup runs as the agent prunes their records (R05-D9); an
        // event's debounce row once it can't repeat: cleared, or one that
        // only notifies once (R05-D12).
        BackupRun::query()->where('status', '!=', BackupRun::RUNNING)->where('started_at', '<', $before)->delete();
        NotificationState::query()->where('last_sent_at', '<', $before)
            ->where(fn ($q) => $q->whereNotNull('cleared_at')->orWhere(fn ($q) => $q->where('event_key', 'not like', 'warning:%')->where('event_key', 'not like', 'agent:%')))
            ->delete();

        $this->line("Deleted {$jobs} jobs from before {$before->toDateTimeString()}, {$actions} agent actions from before {$actionsBefore->toDateTimeString()} and {$deliveries} webhook deliveries.");

        return self::SUCCESS;
    }
}
