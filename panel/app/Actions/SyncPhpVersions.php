<?php

namespace App\Actions;

use App\Agent\ServerStatus;
use App\Models\PhpVersion;
use Illuminate\Support\Facades\DB;

/**
 * Brings php_versions in line with system.status (§7.5): what is installed,
 * the default, the panel's version, which are in use, and each end date
 * (D19). It runs when the PHP versions page loads and after every php.*
 * outcome. A row whose job hasn't finished keeps its status, for the job's
 * outcome to set.
 */
final class SyncPhpVersions
{
    /** False when the agent didn't answer; nothing is changed then. */
    public function run(): bool
    {
        $status = ServerStatus::fresh();
        if ($status === null) {
            return false;
        }
        $php = $status->data['php'];
        $installed = array_column($php['installed'], null, 'version');
        $ends = array_column($php['available'], 'security_end', 'version');

        DB::transaction(function () use ($php, $installed, $ends): void {
            $rows = PhpVersion::all()->keyBy('version');
            foreach (array_keys($installed) as $version) {
                $rows[$version] ??= new PhpVersion(['version' => $version, 'status' => PhpVersion::INSTALLED]);
            }
            foreach ($rows as $version => $row) {
                $version = (string) $version;
                $row->fill([
                    'is_default' => $version === $php['default'],
                    'is_panel' => $version === $php['panel'],
                    'in_use' => $installed[$version]['in_use'] ?? false,
                    'eol_date' => $ends[$version] ?? null,
                ]);
                if ($row->exists && $row->hasUnfinishedJob()) {
                    $row->save();

                    continue;
                }
                if (isset($installed[$version])) {
                    $row->status = PhpVersion::INSTALLED;
                } elseif (in_array($row->status, [PhpVersion::INSTALLING, PhpVersion::FAILED], true)) {
                    // An install that ended without installing it, outcome or not.
                    $row->status = PhpVersion::FAILED;
                } else {
                    $row->delete();

                    continue;
                }
                $row->save();
            }
        });

        return true;
    }
}
