<?php

namespace Tests\Feature\Platform;

use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\File;
use Tests\TestCase;

final class DatabaseTest extends TestCase
{
    use EvaluatesConfigFiles;

    /**
     * §7.1: the panel's SQLite file runs in WAL mode, waits up to 5 seconds
     * for a lock and syncs NORMAL (1). The tests' own database is in memory,
     * which has no journal, so this opens a real file.
     */
    public function testDatabaseUsesWal(): void
    {
        $dir = sys_get_temp_dir().DIRECTORY_SEPARATOR.'zimam-wal-'.bin2hex(random_bytes(6));
        File::ensureDirectoryExists($dir);
        $file = $dir.DIRECTORY_SEPARATOR.'database.sqlite';
        touch($file);
        config(['database.connections.wal_check' => ['database' => $file] + config('database.connections.sqlite')]);

        try {
            $db = DB::connection('wal_check');
            $pragma = fn (string $name) => array_values((array) $db->selectOne("PRAGMA {$name}"))[0];

            $this->assertSame('wal', $pragma('journal_mode'));
            $this->assertSame(5000, (int) $pragma('busy_timeout'));
            $this->assertSame(1, (int) $pragma('synchronous'));
        } finally {
            DB::purge('wal_check');
            File::deleteDirectory($dir);
        }
    }

    /**
     * §7.1: the queue, the cache and the sessions live in SQLite too, so the
     * panel depends on no other service. That holds with the server's .env
     * naming them or not.
     */
    public function testStoresUseDatabase(): void
    {
        $unset = ['QUEUE_CONNECTION' => null, 'CACHE_STORE' => null, 'SESSION_DRIVER' => null];
        $this->assertSame('database', $this->evaluateConfig('queue.php', $unset)['default']);
        $this->assertSame('database', $this->evaluateConfig('cache.php', $unset)['default']);
        $this->assertSame('database', $this->evaluateConfig('session.php', $unset)['driver']);

        $example = file_get_contents(base_path('.env.example'));
        foreach (['QUEUE_CONNECTION', 'CACHE_STORE', 'SESSION_DRIVER'] as $name) {
            $this->assertMatchesRegularExpression("/^{$name}=database\$/m", $example);
        }
    }
}
