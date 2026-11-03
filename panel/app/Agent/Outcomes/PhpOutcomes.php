<?php

namespace App\Agent\Outcomes;

use App\Actions\SyncPhpVersions;
use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\PhpVersion;
use Illuminate\Support\Facades\DB;

/**
 * What php.install, php.uninstall and php.default change in php_versions
 * (§7.4). Each outcome then syncs with system.status, once the outcome's
 * transaction has committed, so the agent call never holds SQLite's lock.
 */
final class PhpOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('php.install', function (AgentJob $job, AgentJobStatus $status): void {
            self::related($job)?->update([
                'status' => $status->status === AgentJob::SUCCEEDED ? PhpVersion::INSTALLED : PhpVersion::FAILED,
            ]);
            self::syncAfterCommit();
        });
        $outcomes->register('php.uninstall', function (AgentJob $job, AgentJobStatus $status): void {
            $version = self::related($job);
            if ($status->status === AgentJob::SUCCEEDED) {
                $version?->delete();
            } else {
                $version?->update(['status' => PhpVersion::INSTALLED]);
            }
            self::syncAfterCommit();
        });
        // The default moves in the sync.
        $outcomes->register('php.default', function (): void {
            self::syncAfterCommit();
        });
    }

    private static function related(AgentJob $job): ?PhpVersion
    {
        $related = $job->related;

        return $related instanceof PhpVersion ? $related : null;
    }

    private static function syncAfterCommit(): void
    {
        DB::afterCommit(fn () => app(SyncPhpVersions::class)->run());
    }
}
