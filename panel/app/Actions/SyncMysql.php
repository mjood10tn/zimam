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
 * reports is removed and one the panel lacked is added. A row that is being
 * created or deleted keeps its status, for its job's outcome to set. run()
 * is for the MySQL page and the outcomes; a site's Database tab uses
 * runForSite(), which asks for that site's databases only and changes
 * nothing else.
 */
final class SyncMysql
{
    /**
     * How long a created or dropped row may wait for its job row before the
     * sync treats it as stuck: the row is saved just before the job is.
     */
    private const IN_FLIGHT_GRACE_MINUTES = 5;

    /** False when the agent didn't answer; nothing is changed then. */
    public function run(): bool
    {
        $status = ServerStatus::fresh();
        if ($status === null) {
            return false;
        }
        $mysql = $status->data['services']['mysql'];
        SyncServiceRow::run(ServerService::MYSQL, $mysql);

        return ! $mysql['installed'] || $this->syncDatabases(new DbList);
    }

    /** One site's databases, from the cached status: false when the agent didn't answer. */
    public function runForSite(Site $site): bool
    {
        $status = ServerStatus::current();
        if ($status === null) {
            return false;
        }

        return ! $status->data['services']['mysql']['installed'] || $this->syncDatabases(new DbList($site->name), $site);
    }

    private function syncDatabases(DbList $command, ?Site $scope = null): bool
    {
        try {
            $listed = app(AgentClient::class)->call($command)->data['databases'];
        } catch (AgentException $e) {
            if (! $e instanceof AgentUnavailable) {
                report($e);
            }

            return false;
        }
        DB::transaction(function () use ($listed, $scope): void {
            $sites = Site::pluck('id', 'name');
            $rows = Database::when($scope, fn ($query) => $query->where('site_id', $scope->id))->get()->keyBy('name');
            $seen = [];
            foreach ($listed as $database) {
                $seen[$database['name']] = true;
                $row = $rows[$database['name']] ?? null;
                if ($row === null) {
                    // A database of a site the panel doesn't know has no row to hang on.
                    if (isset($sites[$database['site']]) && ($scope === null || $scope->id === $sites[$database['site']])) {
                        Database::create(['site_id' => $sites[$database['site']], 'name' => $database['name'], 'size_bytes' => $database['size_bytes'], 'status' => Database::ACTIVE]);
                    }

                    continue;
                }
                $row->size_bytes = $database['size_bytes'];
                if (! $this->inFlight($row) && $row->status !== Database::ACTIVE) {
                    // Back to a database that exists: what went wrong before no longer applies.
                    $row->status = Database::ACTIVE;
                    $row->last_error = null;
                }
                // The sync's own save mustn't restart the wait of a row that has no job.
                $row->timestamps = false;
                $row->save();
            }
            foreach ($rows as $name => $row) {
                // A create in flight has no database yet; a failed create stays to show why.
                if (! isset($seen[$name]) && $row->status !== Database::FAILED && ! $this->inFlight($row)) {
                    $row->delete();
                }
            }
        });

        return true;
    }

    /**
     * Whether the row is being created or deleted: its job hasn't ended, or
     * hasn't been recorded yet (the row is saved just before it).
     */
    private function inFlight(Database $row): bool
    {
        if ($row->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists()) {
            return true;
        }

        return in_array($row->status, [Database::CREATING, Database::DROPPING], true)
            && ! $row->jobs()->exists()
            && $row->updated_at->gt(now()->subMinutes(self::IN_FLIGHT_GRACE_MINUTES));
    }
}
