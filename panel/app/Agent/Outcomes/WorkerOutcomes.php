<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteWorker;

/**
 * site.workers' outcome (v0.4.0). The Workers tab stores the list it
 * submits, marked applying, as the Security tab stores its users; the
 * outcome marks the rows active, or failed with the site's last error, so
 * the owner sees the list didn't take and saves it again (the agent's
 * reconcile converges on a retry, R04-D3). site.workers.restart changes no
 * row: its job's log says how it went.
 */
final class WorkerOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('site.workers', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            $succeeded = $status->status === AgentJob::SUCCEEDED;
            $site->workers()->where('status', SiteWorker::APPLYING)->update(['status' => $succeeded ? SiteWorker::ACTIVE : SiteWorker::FAILED]);
            $site->update(['last_error' => match (true) {
                $succeeded => null,
                $status->status === AgentJob::CANCELLED => __('zimam.workers.cancelled'),
                default => $status->message,
            }]);
        });
    }
}
