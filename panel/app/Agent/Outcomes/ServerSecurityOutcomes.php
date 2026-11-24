<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Fail2banJail;
use App\Settings\PanelSettings;

/**
 * fail2ban.config and cloudflare.mode (R06-D5, D7, D9): once applied, the
 * panel's copy follows. The origin restriction is a guarded change, so its
 * new value is stored only when the owner keeps it (GuardedChangeModal).
 */
final class ServerSecurityOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('fail2ban.config', function (AgentJob $job, AgentJobStatus $status): void {
            if ($status->status !== AgentJob::SUCCEEDED) {
                return;
            }
            foreach ($job->params['jails'] as $jail) {
                Fail2banJail::query()->updateOrCreate(['name' => $jail['name']], [
                    'enabled' => $jail['enabled'], 'max_retry' => $jail['max_retry'], 'find_time' => $jail['find_time'], 'ban_time' => $jail['ban_time'],
                ]);
            }
            app(PanelSettings::class)->setFail2banIgnore($job->params['ignore']);
        });
        $outcomes->register('cloudflare.mode', function (AgentJob $job, AgentJobStatus $status): void {
            if ($status->status !== AgentJob::SUCCEEDED) {
                return;
            }
            $settings = app(PanelSettings::class);
            $guarded = isset($status->result['change']);
            $settings->setCloudflare($job->params['enabled'], $guarded ? $settings->cloudflare()['restrict_origin'] : $job->params['restrict_origin']);
            GuardedChangeOutcomes::record($job, $status);
        });
    }
}
