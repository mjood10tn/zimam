<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Site;

/** What site.provision's outcome changes in sites (§7.4, §9.2). */
final class SiteOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('site.provision', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            if ($status->status === AgentJob::SUCCEEDED) {
                $site->update([
                    'status' => Site::ACTIVE,
                    'template_version' => is_array($status->result) ? ($status->result['template_version'] ?? null) : null,
                    'last_error' => null,
                ]);

                return;
            }
            $site->update([
                'status' => Site::FAILED,
                'last_error' => $status->status === AgentJob::CANCELLED ? __('zimam.sites.cancelled') : $status->message,
            ]);
        });
    }
}
