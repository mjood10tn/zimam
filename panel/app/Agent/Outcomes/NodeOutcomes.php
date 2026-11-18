<?php

namespace App\Agent\Outcomes;

use App\Agent\JobOutcomes;
use App\Agent\ServerStatus;
use Illuminate\Support\Facades\DB;

/**
 * Node is read live from system.status, never stored (ledger R1), so a Node
 * job's outcome only refreshes the cached status: the page then shows the new
 * state at once instead of up to 10 seconds late. It asks after the outcome's
 * transaction has committed, so the agent call never holds SQLite's lock.
 */
final class NodeOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        foreach (['node.install', 'node.uninstall'] as $command) {
            $outcomes->register($command, function (): void {
                DB::afterCommit(fn () => ServerStatus::fresh());
            });
        }
    }
}
