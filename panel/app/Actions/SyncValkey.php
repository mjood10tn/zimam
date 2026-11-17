<?php

namespace App\Actions;

use App\Agent\ServerStatus;
use App\Models\ServerService;

/** Brings the valkey row of server_services in line with the agent (system.status). */
final class SyncValkey
{
    /** False when the agent didn't answer; nothing is changed then. */
    public function run(): bool
    {
        $status = ServerStatus::fresh();
        if ($status === null) {
            return false;
        }
        SyncServiceRow::run(ServerService::VALKEY, $status->service(ServerService::VALKEY) ?? ['installed' => false, 'version' => '', 'active' => false]);

        return true;
    }
}
