<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Agent\ServerStatus;
use App\Models\AgentJob;
use App\Models\OsUpdateRun;
use Illuminate\Support\Facades\DB;

/**
 * OS updates and reboots (R06-D12..D16). An install's run row takes what the
 * agent reports. Every one of these changes what system.status says (the
 * counts, the reboot, the automatic updates), so the cached status is read
 * again once the outcome's transaction commits: the dashboard's counts then
 * refresh without waiting for the hourly count (§7.5).
 */
final class OsUpdateOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('updates.install', function (AgentJob $job, AgentJobStatus $status): void {
            $run = $job->related;
            if ($run instanceof OsUpdateRun) {
                $ok = $status->status === AgentJob::SUCCEEDED && is_array($status->result);
                $run->update([
                    'status' => $ok ? OsUpdateRun::SUCCEEDED : OsUpdateRun::FAILED,
                    'upgraded' => $ok ? $status->result['upgraded'] : null,
                    'restarted' => $ok ? $status->result['restarted'] : null,
                    'reboot_required' => $ok ? $status->result['reboot_required'] : null,
                    'started_at' => $status->startedAt ?? $run->started_at,
                    'finished_at' => now(),
                ]);
            }
            DB::afterCommit(fn () => ServerStatus::fresh());
        });
        foreach (['updates.auto', 'system.reboot', 'system.reboot.cancel'] as $command) {
            $outcomes->register($command, function (): void {
                DB::afterCommit(fn () => ServerStatus::fresh());
            });
        }
    }
}
