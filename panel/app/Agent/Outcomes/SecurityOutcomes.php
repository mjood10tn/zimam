<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Certificate;
use App\Models\SecurityFinding;
use App\Models\Site;
use Carbon\CarbonImmutable;

/**
 * security.check's outcome (§7.4, §9.7): the latest finding of each check
 * replaces the last one, and certificate_valid's expiry updates the site's
 * certificate, which is how the panel learns about renewals (§10).
 */
final class SecurityOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('security.check', function (AgentJob $job, AgentJobStatus $status): void {
            if ($status->status !== AgentJob::SUCCEEDED || ! is_array($status->result)) {
                return;
            }
            $site = $job->related instanceof Site ? $job->related : null;
            $scope = $status->result['scope'] ?? ($site === null ? 'server' : 'site');
            $now = CarbonImmutable::now();
            foreach ($status->result['findings'] ?? [] as $f) {
                SecurityFinding::query()->updateOrCreate(
                    ['scope' => $scope, 'site_id' => $site?->id, 'check' => $f['check']],
                    ['status' => $f['status'], 'detail' => $f['detail'], 'checked_at' => $now],
                );
                if ($f['check'] === 'certificate_valid' && $site !== null && is_string($f['expires_at'] ?? null)) {
                    $site->certificate()->where('status', Certificate::ISSUED)->first()?->update(['expires_at' => CarbonImmutable::parse($f['expires_at'])]);
                }
            }
        });
    }
}
