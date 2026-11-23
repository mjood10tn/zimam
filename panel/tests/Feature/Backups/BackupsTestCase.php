<?php

namespace Tests\Feature\Backups;

use App\Models\BackupDestination;
use App\Models\BackupPlan;
use Tests\Feature\Databases\DatabasesTestCase;

/** What the backup tests share, on top of the MySQL tests' fake agent, owner and sites. */
abstract class BackupsTestCase extends DatabasesTestCase
{
    protected const S3 = ['endpoint' => 'https://s3.example.com', 'region' => 'us-east-1', 'bucket' => 'zimam-backups', 'prefix' => 'one'];

    /** A destination on the fake's server and in the panel, as a saved one is. */
    protected function destination(string $key = 'offsite', string $type = BackupDestination::S3): BackupDestination
    {
        $this->agent->backupDestination($key, $type);

        return BackupDestination::create([
            'key' => $key,
            'type' => $type,
            'settings' => $type === BackupDestination::S3 ? self::S3 : ['host' => 'backup.example.com', 'port' => 22, 'user' => 'zimam', 'path' => ''],
            'status' => BackupDestination::ACTIVE,
        ]);
    }

    /** A plan on the fake's server and in the panel, as a saved one is. */
    protected function plan(BackupDestination $destination, string $key = 'nightly', array $sites = [], array $databases = []): BackupPlan
    {
        $plan = BackupPlan::create([
            'key' => $key,
            'backup_destination_id' => $destination->id,
            'schedule' => 'daily 02:00',
            'retention' => ['keep_last' => 7],
            'status' => BackupPlan::ACTIVE,
        ]);
        $plan->sites()->sync(collect($sites)->pluck('id'));
        $plan->databases()->sync(collect($databases)->pluck('id'));
        $this->agent->backupPlan($key, [
            'destination' => $destination->key,
            'sites' => collect($sites)->pluck('name')->all(),
            'databases' => collect($databases)->pluck('name')->all(),
        ]);

        return $plan;
    }
}
