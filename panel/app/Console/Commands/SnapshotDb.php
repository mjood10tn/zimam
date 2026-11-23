<?php

namespace App\Console\Commands;

use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;

/**
 * Writes a consistent copy of the panel's database to <path> with SQLite's
 * VACUUM INTO, for a backup plan's panel part (R05-D5). The agent runs it
 * as zimam through the helper with /home/zimam/panel/shared/zimam-backup.sqlite,
 * removes a leftover copy before and the copy after, and archives it. VACUUM
 * INTO reads one snapshot, so the queue may keep writing meanwhile (Task 1).
 */
final class SnapshotDb extends Command
{
    protected $signature = 'zimam:snapshot-db {path : absolute path of the copy; must not exist}';

    protected $description = 'Write a consistent copy of the panel database';

    public function handle(): int
    {
        $path = (string) $this->argument('path');
        // A drive letter counts too, for the tests on Windows.
        if (! str_starts_with($path, '/') && preg_match('/\A[A-Za-z]:[\\\\\/]/', $path) !== 1) {
            $this->error('The path must be absolute.');

            return self::FAILURE;
        }
        if (file_exists($path)) {
            $this->error('The path already exists.');

            return self::FAILURE;
        }
        DB::statement('VACUUM INTO ?', [$path]);
        $this->line("Wrote {$path}.");

        return self::SUCCESS;
    }
}
