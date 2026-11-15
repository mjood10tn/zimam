<?php

namespace Tests\Unit;

use App\Support\PanelLog;
use PHPUnit\Framework\TestCase;

final class PanelLogTest extends TestCase
{
    /** The full 16 MiB search window of short lines keeps only the matches, not every line. */
    public function testSearchOfFullWindowStaysUnderMemoryLimit(): void
    {
        $path = tempnam(sys_get_temp_dir(), 'laravel');
        $line = str_repeat('x', 15)."\n";
        $count = (16 << 20) / 16 - 1;
        file_put_contents($path, str_repeat($line, $count)."needle\n");

        gc_collect_cycles();
        memory_reset_peak_usage();
        $before = memory_get_peak_usage(true);
        $result = (new PanelLog($path))->read(search: 'needle');
        $growth = memory_get_peak_usage(true) - $before;
        unlink($path);

        $this->assertSame([['offset' => (16 << 20) - 16, 'text' => 'needle']], $result['lines']);
        $this->assertSame('end', $result['search_stopped']);
        $this->assertLessThan(64 << 20, $growth, 'a full-window search grew memory by '.round($growth / (1 << 20)).' MiB');
    }
}
