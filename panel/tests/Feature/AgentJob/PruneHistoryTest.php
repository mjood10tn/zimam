<?php

namespace Tests\Feature\AgentJob;

use App\Models\AgentAction;
use App\Models\AgentJob;
use App\Models\Setting;
use Illuminate\Console\Scheduling\Event;
use Illuminate\Console\Scheduling\Schedule;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Artisan;
use Tests\TestCase;

/** zimam:prune-history keeps job and action rows for the log retention period (§7.1). */
final class PruneHistoryTest extends TestCase
{
    use RefreshDatabase;

    public function testDeletesRowsOlderThanRetention(): void
    {
        $this->freezeSecond();
        $old = $this->job(days: 15, status: AgentJob::SUCCEEDED);
        $recent = $this->job(days: 13, status: AgentJob::FAILED);
        $edge = $this->job(days: 14, status: AgentJob::SUCCEEDED, seconds: -1);
        $oldAction = $this->action(days: 15);
        $recentAction = $this->action(days: 13);

        $this->assertSame(0, Artisan::call('zimam:prune-history'));

        $this->assertModelMissing($old);
        $this->assertModelExists($recent);
        $this->assertModelExists($edge);
        // The panel's action log follows the audit log's own retention, a year by default (D24).
        $this->assertModelExists($oldAction);
        $this->assertModelExists($recentAction);
        $yearOld = $this->action(days: 366);
        Artisan::call('zimam:prune-history');
        $this->assertModelMissing($yearOld);
        $this->assertModelExists($oldAction);
    }

    public function testFollowsTheRetentionSetting(): void
    {
        $this->freezeSecond();
        Setting::query()->updateOrCreate(['key' => 'log_retention_days'], ['value' => 3]);
        Setting::query()->updateOrCreate(['key' => 'audit_retention_days'], ['value' => 3]);
        $job = $this->job(days: 4, status: AgentJob::CANCELLED);
        $action = $this->action(days: 4);
        $kept = $this->action(days: 2);

        Artisan::call('zimam:prune-history');

        $this->assertModelMissing($job);
        $this->assertModelMissing($action);
        $this->assertModelExists($kept);
    }

    /** A job the panel still waits on or tracks is never pruned, however old. */
    public function testKeepsUnfinishedJobs(): void
    {
        $waiting = $this->job(days: 30, status: AgentJob::WAITING);
        $running = $this->job(days: 30, status: AgentJob::RUNNING);

        Artisan::call('zimam:prune-history');

        $this->assertModelExists($waiting);
        $this->assertModelExists($running);
    }

    public function testRunsDaily(): void
    {
        $events = collect(app(Schedule::class)->events())
            ->filter(fn (Event $event): bool => str_contains((string) $event->command, 'zimam:prune-history'));

        $this->assertCount(1, $events);
        $this->assertSame('0 0 * * *', $events->first()->expression);
    }

    private function job(int $days, string $status, int $seconds = 0): AgentJob
    {
        $job = AgentJob::query()->create(['command' => 'e2e.sleep', 'status' => $status, 'params' => []]);
        $job->forceFill(['created_at' => now()->subDays($days)->subSeconds($seconds)])->save();

        return $job;
    }

    private function action(int $days): AgentAction
    {
        $action = AgentAction::query()->create(['command' => 'cert.issue', 'params' => [], 'outcome' => 'accepted', 'duration_ms' => 3]);
        $action->forceFill(['created_at' => now()->subDays($days)])->save();

        return $action;
    }
}
