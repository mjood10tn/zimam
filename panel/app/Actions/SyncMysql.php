<?php

namespace App\Actions;

use App\Agent\AgentClient;
use App\Agent\Commands\DbList;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\ServerStatus;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\ServerService;
use App\Models\Site;
use Illuminate\Support\Facades\DB;

/**
 * Brings server_services and databases in line with the agent (system.status
 * and db.list): the agent's list is the truth, so a database it no longer
 * reports is removed and one the panel lacked is added. A row whose job is
 * still running keeps its status, for the job's outcome to set. It runs
 * when the MySQL page or a site's Database tab loads, and after every
 * MySQL outcome.
 */
final class SyncMysql
{
    /** False when the agent didn't answer; nothing is changed then. */
    public function run(): bool
    {
        $status = ServerStatus::fresh();
        if ($status === null) {
            return false;
        }
        $mysql = $status->data['services']['mysql'];
        $this->syncService($mysql);
        if (! $mysql['installed']) {
            return true;
        }
        try {
            $listed = app(AgentClient::class)->call(new DbList)->data['databases'];
        } catch (AgentException $e) {
            if (! $e instanceof AgentUnavailable) {
                report($e);
            }

            return false;
        }
        $this->syncDatabases($listed);

        return true;
    }

    /** @param array{installed: bool, version: string, active: bool} $mysql */
    private function syncService(array $mysql): void
    {
        $row = ServerService::named(ServerService::MYSQL);
        if ($row !== null && $row->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists()) {
            return;
        }
        if ($mysql['installed']) {
            ServerService::updateOrCreate(
                ['name' => ServerService::MYSQL],
                ['status' => ServerService::INSTALLED, 'version' => $mysql['version'], 'last_error' => null],
            );
        } elseif ($row !== null && in_array($row->status, [ServerService::INSTALLING, ServerService::FAILED], true)) {
            // An install that ended without installing it, outcome or not.
            $row->update(['status' => ServerService::FAILED]);
        } else {
            $row?->delete();
        }
    }

    /** @param list<array{name: string, site: string, size_bytes: int}> $listed */
    private function syncDatabases(array $listed): void
    {
        DB::transaction(function () use ($listed): void {
            $sites = Site::pluck('id', 'name');
            $rows = Database::all()->keyBy('name');
            $seen = [];
            foreach ($listed as $database) {
                $seen[$database['name']] = true;
                $row = $rows[$database['name']] ?? null;
                if ($row === null) {
                    // A database of a site the panel doesn't know has no row to hang on.
                    if (isset($sites[$database['site']])) {
                        Database::create(['site_id' => $sites[$database['site']], 'name' => $database['name'], 'size_bytes' => $database['size_bytes'], 'status' => Database::ACTIVE]);
                    }

                    continue;
                }
                $row->size_bytes = $database['size_bytes'];
                $row->site_id = $sites[$database['site']] ?? $row->site_id;
                if (! $row->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists()) {
                    $row->status = Database::ACTIVE;
                }
                $row->save();
            }
            foreach ($rows as $name => $row) {
                // A create in flight has no database yet; a failed create stays to show why.
                if (! isset($seen[$name]) && $row->status !== Database::FAILED && ! $row->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists()) {
                    $row->delete();
                }
            }
        });
    }
}
