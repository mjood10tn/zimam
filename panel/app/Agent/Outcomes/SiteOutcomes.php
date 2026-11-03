<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Site;

/** What site.provision's and site.php's outcomes change in sites (§7.4, §9.2, §9.5). */
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
        // A switch that didn't finish leaves the site serving on its old
        // version (§9.5), so it stays active; only the reason is kept.
        $outcomes->register('site.php', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if (! $site instanceof Site) {
                return;
            }
            if ($status->status === AgentJob::SUCCEEDED && is_array($status->result) && isset($status->result['php_version'])) {
                $site->update(['php_version' => $status->result['php_version'], 'last_error' => null]);

                return;
            }
            $site->update([
                'last_error' => $status->status === AgentJob::CANCELLED ? __('zimam.sites.switch.cancelled') : $status->message,
            ]);
        });
    }
}
