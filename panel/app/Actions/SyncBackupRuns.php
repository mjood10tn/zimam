<?php

namespace App\Actions;

use App\Agent\AgentClient;
use App\Agent\Commands\BackupRuns;
use App\Models\BackupPlan;
use App\Models\BackupRun;
use Carbon\CarbonImmutable;

/**
 * Copies the agent's run records into backup_runs (R05-D9). backup.runs
 * gives the records after a backup id; a record still running changes
 * later, so the poll starts just before the oldest run the panel still
 * has as running, else after the newest it has. Records of plans the
 * panel doesn't know are skipped.
 */
final class SyncBackupRuns
{
    /**
     * @return list<BackupRun> the runs that finished since the last sync, not yet notified
     */
    public function run(): array
    {
        $plans = BackupPlan::pluck('id', 'key')->all();
        if ($plans === []) {
            return [];
        }
        $running = BackupRun::where('status', BackupRun::RUNNING)->min('backup_id');
        $since = $running !== null
            ? CarbonImmutable::createFromFormat(BackupRun::ID_FORMAT, $running, 'UTC')->subSecond()->format(BackupRun::ID_FORMAT)
            : BackupRun::max('backup_id');
        $records = app(AgentClient::class)->call(new BackupRuns(since: $since))->data['runs'] ?? [];

        $finished = [];
        foreach ($records as $record) {
            if (! isset($plans[$record['plan']])) {
                continue;
            }
            $run = BackupRun::firstOrNew(['backup_plan_id' => $plans[$record['plan']], 'backup_id' => $record['backup']]);
            $wasFinished = $run->exists && $run->status !== BackupRun::RUNNING;
            $run->fill([
                'trigger' => $record['trigger'],
                'status' => $record['status'],
                'parts' => $record['parts'],
                'size_bytes' => array_sum(array_column($record['parts'], 'bytes')),
                'error' => $record['error'] ?? null,
                'started_at' => CarbonImmutable::parse($record['started_at']),
                'finished_at' => isset($record['finished_at']) ? CarbonImmutable::parse($record['finished_at']) : null,
            ])->save();
            if (! $wasFinished && $run->status !== BackupRun::RUNNING && $run->notified_at === null) {
                $finished[] = $run;
            }
        }

        return $finished;
    }
}
