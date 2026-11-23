<?php

namespace App\Console\Commands;

use App\Actions\SyncBackupRuns as SyncRuns;
use App\Agent\Exceptions\AgentException;
use Illuminate\Console\Command;

/**
 * Reads the agent's backup run records into the panel every minute
 * (R05-D9): runs from the plans' timers have no panel job to follow. An
 * unreachable agent is tried again on the next minute.
 */
final class SyncBackupRuns extends Command
{
    protected $signature = 'zimam:sync-backup-runs';

    protected $description = 'Copy the agent\'s backup run records into the panel';

    public function handle(SyncRuns $sync): int
    {
        try {
            $finished = $sync->run();
        } catch (AgentException $e) {
            $this->warn('The agent did not answer: '.$e->getMessage());

            return self::SUCCESS;
        }
        $this->line(count($finished).' runs finished since the last sync.');

        return self::SUCCESS;
    }
}
