<?php

namespace App\Agent\Outcomes;

use App\Actions\SyncBackupRuns;
use App\Actions\SyncMysql;
use App\Agent\AgentJobStatus;
use App\Agent\Exceptions\AgentException;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\BackupDestination;
use App\Models\BackupPlan;
use Illuminate\Support\Facades\DB;

/**
 * What the backup jobs change in backup_destinations, backup_plans and
 * backup_runs. A run's record comes from backup.runs, read once the
 * outcome's transaction has committed so the agent call never holds
 * SQLite's lock; a restore into a new database syncs MySQL the same way.
 */
final class BackupOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('backup.destination', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof BackupDestination) {
                $row->update($status->status === AgentJob::SUCCEEDED
                    ? ['status' => BackupDestination::ACTIVE, 'last_error' => null, 'public_key' => $status->result['public_key'] ?? $row->public_key]
                    : ['status' => BackupDestination::FAILED, 'last_error' => DatabaseOutcomes::reason($status, 'backups.cancelled')]);
            }
        });
        $outcomes->register('backup.destination.test', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof BackupDestination) {
                $row->update($status->status === AgentJob::SUCCEEDED
                    ? ['last_tested_at' => now(), 'last_error' => null, 'host_key_fingerprint' => $status->result['host_key_fingerprint'] ?? $row->host_key_fingerprint]
                    : ['last_error' => DatabaseOutcomes::reason($status, 'backups.cancelled')]);
            }
        });
        $outcomes->register('backup.destination.delete', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof BackupDestination) {
                $status->status === AgentJob::SUCCEEDED
                    ? $row->delete()
                    : $row->update(['status' => BackupDestination::ACTIVE, 'last_error' => DatabaseOutcomes::reason($status, 'backups.cancelled')]);
            }
        });
        $outcomes->register('backup.plan', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof BackupPlan) {
                $row->update($status->status === AgentJob::SUCCEEDED
                    ? ['status' => BackupPlan::ACTIVE, 'last_error' => null]
                    : ['status' => BackupPlan::FAILED, 'last_error' => DatabaseOutcomes::reason($status, 'backups.cancelled')]);
            }
        });
        $outcomes->register('backup.plan.delete', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof BackupPlan) {
                $status->status === AgentJob::SUCCEEDED
                    ? $row->delete()
                    : $row->update(['status' => BackupPlan::ACTIVE, 'last_error' => DatabaseOutcomes::reason($status, 'backups.cancelled')]);
            }
        });
        $outcomes->register('backup.run', function (): void {
            DB::afterCommit(function (): void {
                try {
                    app(SyncBackupRuns::class)->run();
                } catch (AgentException) {
                    // The minute's sync reads it.
                }
            });
        });
        $outcomes->register('backup.restore', function (AgentJob $job, AgentJobStatus $status): void {
            if ($status->status === AgentJob::SUCCEEDED && isset($status->result['database'])) {
                DB::afterCommit(fn () => app(SyncMysql::class)->run());
            }
        });
    }
}
