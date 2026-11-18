<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteWorker;

/**
 * site.workers' outcome (v0.4.0). The Workers tab stores the list it
 * submits, marked applying, as the Security tab stores its users, and
 * marks the workers it left out removing. Success deletes the removing rows
 * and marks the rest active. Failure marks both failed, with the site's
 * last error, so a worker that may still run stays on the tab and the
 * owner saves or deletes again (the agent's reconcile converges on a
 * retry, R04-D3). site.workers.restart changes no row: its job's log
 * says how it went.
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
            if ($succeeded) {
                $site->workers()->where('status', SiteWorker::REMOVING)->delete();
                $site->workers()->where('status', SiteWorker::APPLYING)->update(['status' => SiteWorker::ACTIVE]);
            } else {
                // A worker whose removal failed may still run: its row stays, to remove again.
                $site->workers()->whereIn('status', [SiteWorker::APPLYING, SiteWorker::REMOVING])->update(['status' => SiteWorker::FAILED]);
            }
            $site->update(['last_error' => match (true) {
                $succeeded => null,
                $status->status === AgentJob::CANCELLED => __('zimam.workers.cancelled'),
                default => $status->message,
            }]);
        });
    }
}
