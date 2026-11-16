<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Site;

/**
 * What site.repo's, site.rollback's and site.env.write's outcomes change
 * (R02-D4, R02-D5, R02-D10).
 * site.deploy's are on its deployments row, which follows its job through
 * every change (Deployment::follow), so a failed build leaves the site as
 * it is: its live release still serves.
 */
final class DeploymentOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        // The repository, branch and key are stored once the agent has them;
        // a failed connect keeps the ones before it.
        $outcomes->register('site.repo', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            $result = $status->result;
            if ($status->status !== AgentJob::SUCCEEDED || ! is_array($result)) {
                $site->update(['last_error' => $status->status === AgentJob::CANCELLED ? __('zimam.deployments.repository.cancelled') : $status->message]);

                return;
            }
            $site->repository?->update([
                'repository' => $result['repository'],
                'branch' => $result['branch'],
                'deploy_key_public' => $result['public_key'],
                'deploy_key_fingerprint' => $result['fingerprint'],
            ]);
            $site->update(['last_error' => null]);
        });
        // A failed .env save is the site's last error; its message names the
        // file and the reason, never the content (R02-D10).
        $outcomes->register('site.env.write', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            $site->update(['last_error' => match ($status->status) {
                AgentJob::SUCCEEDED => null,
                AgentJob::CANCELLED => __('zimam.environment.cancelled'),
                default => $status->message,
            }]);
        });
        $outcomes->register('site.rollback', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            $site->update(['last_error' => match ($status->status) {
                AgentJob::SUCCEEDED => null,
                AgentJob::CANCELLED => __('zimam.deployments.rollback.cancelled'),
                default => $status->message,
            }]);
        });
    }
}
