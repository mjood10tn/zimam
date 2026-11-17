<?php

namespace App\Agent\Outcomes;

use App\Actions\SyncMysql;
use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\ServerService;
use App\Models\Site;
use App\Models\SiteCredential;
use Illuminate\Support\Facades\DB;

/**
 * What mysql.install, db.create, db.drop and db.user change in
 * server_services, databases and site_credentials. Each outcome then syncs
 * with the agent, once its transaction has committed, so the agent call
 * never holds SQLite's lock.
 */
final class DatabaseOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        $outcomes->register('mysql.install', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof ServerService) {
                $row->update($status->status === AgentJob::SUCCEEDED
                    ? ['status' => ServerService::INSTALLED, 'version' => is_array($status->result) ? ($status->result['version'] ?? null) : null, 'last_error' => null]
                    : ['status' => ServerService::FAILED, 'last_error' => self::reason($status, 'databases.install.cancelled')]);
            }
            self::syncAfterCommit();
        });
        $outcomes->register('db.create', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof Database) {
                $row->update($status->status === AgentJob::SUCCEEDED
                    ? ['status' => Database::ACTIVE, 'last_error' => null]
                    : ['status' => Database::FAILED, 'last_error' => self::reason($status, 'databases.create.cancelled')]);
            }
            self::syncAfterCommit();
        });
        // A drop that didn't finish may have left the database: the sync tells.
        $outcomes->register('db.drop', function (AgentJob $job, AgentJobStatus $status): void {
            $row = $job->related;
            if ($row instanceof Database) {
                if ($status->status === AgentJob::SUCCEEDED) {
                    $row->delete();
                } else {
                    $row->update(['status' => Database::ACTIVE, 'last_error' => self::reason($status, 'databases.drop.cancelled')]);
                }
            }
            self::syncAfterCommit();
        });
        // Only the fact that the user exists and when its password changed is
        // kept; a failed rotation leaves the old password working.
        $outcomes->register('db.user', function (AgentJob $job, AgentJobStatus $status): void {
            $site = $job->related;
            if ($site instanceof Site && $status->status === AgentJob::SUCCEEDED) {
                SiteCredential::updateOrCreate(
                    ['site_id' => $site->id, 'kind' => SiteCredential::MYSQL],
                    ['username' => SiteCredential::usernameFor($site->name), 'rotated_at' => now()],
                );
            }
        });
    }

    private static function reason(AgentJobStatus $status, string $cancelledKey): string
    {
        return $status->status === AgentJob::CANCELLED ? __("zimam.{$cancelledKey}") : $status->message;
    }

    private static function syncAfterCommit(): void
    {
        DB::afterCommit(fn () => app(SyncMysql::class)->run());
    }
}
