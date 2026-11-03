<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Certificate;
use Carbon\CarbonImmutable;

/**
 * What cert.issue's outcome changes (§7.4, §10): the certificate's status,
 * expiry and last error, and force_https, which the agent turns on with
 * the HTTPS vhost.
 */
final class CertificateOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('cert.issue', function (AgentJob $job, AgentJobStatus $status): void {
            $certificate = $job->related;
            if (! $certificate instanceof Certificate) {
                return;
            }
            $expiresAt = is_array($status->result) ? ($status->result['expires_at'] ?? null) : null;
            if ($status->status === AgentJob::SUCCEEDED && is_string($expiresAt)) {
                $certificate->update(['status' => Certificate::ISSUED, 'expires_at' => CarbonImmutable::parse($expiresAt), 'last_error' => null]);
                $certificate->site->securitySettings()->update(['force_https' => true]);

                return;
            }
            $certificate->update([
                'status' => Certificate::FAILED,
                'last_error' => $status->status === AgentJob::CANCELLED ? __('zimam.sites.certificate.cancelled') : $status->message,
            ]);
        });
    }
}
