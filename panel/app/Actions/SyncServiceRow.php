<?php

namespace App\Actions;

use App\Models\AgentJob;
use App\Models\ServerService;

/**
 * Brings a server_services row in line with what system.status says of that
 * service. A row whose install job hasn't ended keeps its status, for the
 * job's outcome to set.
 */
final class SyncServiceRow
{
    /** @param array{installed: bool, version: string, active: bool} $service */
    public static function run(string $name, array $service): void
    {
        $row = ServerService::named($name);
        if ($row !== null && $row->jobs()->whereNotIn('status', AgentJob::FINISHED)->exists()) {
            return;
        }
        if ($service['installed']) {
            ServerService::updateOrCreate(
                ['name' => $name],
                ['status' => ServerService::INSTALLED, 'version' => $service['version'], 'last_error' => null],
            );
        } elseif ($row !== null && in_array($row->status, [ServerService::INSTALLING, ServerService::FAILED], true)) {
            // An install that ended without installing it, outcome or not.
            $row->update(['status' => ServerService::FAILED]);
        } else {
            $row?->delete();
        }
    }
}
