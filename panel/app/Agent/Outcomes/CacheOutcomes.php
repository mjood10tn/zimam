<?php

namespace App\Agent\Outcomes;

use App\Actions\SyncValkey;
use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\ServerService;
use App\Models\Site;
use App\Models\SiteCredential;
use Illuminate\Support\Facades\DB;

/**
 * What valkey.install and cache.user change in server_services and
 * site_credentials. The install syncs with the agent once its transaction
 * has committed, so the agent call never holds SQLite's lock.
 */
final class CacheOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('valkey.install', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof ServerService) {
                $row->update($status->status === AgentJob::SUCCEEDED
                    ? ['status' => ServerService::INSTALLED, 'version' => is_array($status->result) ? ($status->result['version'] ?? null) : null, 'last_error' => null]
                    : ['status' => ServerService::FAILED, 'last_error' => DatabaseOutcomes::reason($status, 'cache.install.cancelled')]);
            }
            DB::afterCommit(fn () => app(SyncValkey::class)->run());
        });
        // Only the fact that the user exists and when its password changed is
        // kept; a failed rotation leaves the old password working.
        $outcomes->register('cache.user', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if ($site instanceof Site && $status->status === AgentJob::SUCCEEDED) {
                SiteCredential::updateOrCreate(
                    ['site_id' => $site->id, 'kind' => SiteCredential::VALKEY],
                    ['username' => SiteCredential::usernameFor($site->name), 'rotated_at' => now()],
                );
            }
        });
    }
}
