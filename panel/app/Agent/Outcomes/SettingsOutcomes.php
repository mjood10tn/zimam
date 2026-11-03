<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Settings\PanelSettings;

/** logs.policy's outcome (§7.4, §11.4): the panel stores the policy once the agent has applied it. */
final class SettingsOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('logs.policy', function (AgentJob $job, AgentJobStatus $status): void {
            if ($status->status !== AgentJob::SUCCEEDED) {
                return;
            }
            $p = is_array($status->result) ? $status->result : $job->params;
            app(PanelSettings::class)->setLogPolicy((int) $p['retention_days'], (int) $p['audit_retention_days'], (int) $p['site_cap_bytes']);
        });
    }
}
