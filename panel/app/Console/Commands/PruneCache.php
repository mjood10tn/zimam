<?php

namespace App\Console\Commands;

use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;

/**
 * Deletes expired rows of the database cache store. The store removes an
 * expired row only when that same key is read again, and a throttle key
 * (the hooks' failure counts, the login's) is often never read again, so
 * without this the panel's one SQLite file only grows. Scheduled daily.
 */
final class PruneCache extends Command
{
    protected $signature = 'zimam:prune-cache';

    protected $description = 'Delete expired rows of the database cache and its locks';

    public function handle(): int
    {
        $store = config('cache.stores.'.config('cache.default'));
        if (($store['driver'] ?? null) !== 'database') {
            $this->line('The cache is not in the database; nothing to delete.');

            return self::SUCCESS;
        }
        $now = now()->getTimestamp();
        $table = $store['table'] ?? 'cache';
        $rows = DB::connection($store['connection'] ?? null)->table($table)->where('expiration', '<', $now)->delete();
        $locks = DB::connection($store['lock_connection'] ?? $store['connection'] ?? null)
            ->table($store['lock_table'] ?? 'cache_locks')
            ->where('expiration', '<', $now)
            ->delete();
        $this->line("Deleted {$rows} expired cache rows and {$locks} expired locks.");

        return self::SUCCESS;
    }
}
