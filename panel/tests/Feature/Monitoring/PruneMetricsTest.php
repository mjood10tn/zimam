<?php

namespace Tests\Feature\Monitoring;

use App\Models\MetricSample;
use Illuminate\Console\Scheduling\Event;
use Illuminate\Console\Scheduling\Schedule;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Artisan;
use Tests\Support\MetricSamples;
use Tests\TestCase;

/** zimam:prune-metrics keeps seven days of samples (§7.5). */
final class PruneMetricsTest extends TestCase
{
    use MetricSamples;
    use RefreshDatabase;

    public function testPruneAfter7Days(): void
    {
        $this->freezeSecond();
        $old = $this->sample(now()->subDays(8));
        $edge = $this->sample(now()->subDays(7)->addSecond());
        $recent = $this->sample(now()->subMinute());

        $this->assertSame(0, Artisan::call('zimam:prune-metrics'));

        $this->assertModelMissing($old);
        $this->assertModelExists($edge);
        $this->assertModelExists($recent);
    }

    public function testRunsDaily(): void
    {
        $events = collect(app(Schedule::class)->events())
            ->filter(fn (Event $event): bool => str_contains((string) $event->command, 'zimam:prune-metrics'));

        $this->assertCount(1, $events);
        $this->assertSame('0 0 * * *', $events->first()->expression);
    }
}
