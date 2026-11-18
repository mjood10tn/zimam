<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteCronJob;

/**
 * site.cron's outcome (v0.4.0), the workers' pattern. The Schedule tab
 * stores the list it submits, marked applying, and marks the jobs it left
 * out removing. Success deletes the removing rows, marks the rest active and
 * takes the scheduler's new state from what the job sent: the tab never
 * assumes it before the agent has applied it. Failure marks both failed,
 * with the site's last error, so a job that may still run stays on the tab
 * and the owner saves or deletes again (the agent's reconcile converges on a
 * retry); the scheduler keeps the state it had.
 */
final class CronOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('site.cron', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            $succeeded = $status->status === AgentJob::SUCCEEDED;
            if ($succeeded) {
                $site->cronJobs()->where('status', SiteCronJob::REMOVING)->delete();
                $site->cronJobs()->where('status', SiteCronJob::APPLYING)->update(['status' => SiteCronJob::ACTIVE]);
                $site->scheduler_enabled = (bool) ($job->params['scheduler'] ?? false);
            } else {
                // A job whose removal failed may still run: its row stays, to remove again.
                $site->cronJobs()->whereIn('status', [SiteCronJob::APPLYING, SiteCronJob::REMOVING])->update(['status' => SiteCronJob::FAILED]);
            }
            $site->last_error = match (true) {
                $succeeded => null,
                $status->status === AgentJob::CANCELLED => __('zimam.schedule.cancelled'),
                default => $status->message,
            };
            $site->save();
        });
    }
}
