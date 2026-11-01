<?php

namespace Tests\Unit\Agent;

use App\Agent\Commands\AgentVersion;
use App\Agent\Commands\E2eSleep;
use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentJobFailed;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\FakeAgentClient;
use Tests\TestCase;

/** The fake stands in for the agent in every test and under Herd (§6, §14.6). */
final class FakeAgentClientTest extends TestCase
{
    /**
     * TrackAgentJob never follows a job, so a job must end through status()
     * alone, as a real one does.
     */
    public function testUnscriptedJobFinishesThroughStatusAlone(): void
    {
        $fake = new FakeAgentClient;
        $job = $fake->submit(new E2eSleep(5, 'shop'));

        $seen = [];
        for ($i = 0; $i < 5 && ! ($status = $fake->status($job->id))->isFinished(); $i++) {
            $seen[] = $status->status;
        }

        $this->assertSame(['running'], $seen);
        $this->assertSame('succeeded', $status->status);
        $this->assertNotNull($status->finishedAt);
    }

    public function testScriptedJobLifecycle(): void
    {
        $fake = (new FakeAgentClient)->scriptJob('e2e.sleep', ['queued', 'running', 'running', 'failed'],
            log: ['Sleeping 5 seconds', 'Out of patience'], errorCode: 'internal', message: 'Out of patience');
        $job = $fake->submit(new E2eSleep(5, 'shop'));

        $statuses = array_map(fn () => $fake->status($job->id), range(1, 5));

        $this->assertSame(['queued', 'running', 'running', 'failed', 'failed'], array_map(fn ($s) => $s->status, $statuses));
        $this->assertSame('internal', $statuses[3]->errorCode);
        $this->assertSame('Out of patience', $statuses[3]->message);
        $this->assertGreaterThan(0, $statuses[3]->logSize);

        $lines = [];
        try {
            $fake->follow($job->id, 0, function (string $line) use (&$lines): void {
                $lines[] = $line;
            });
            $this->fail('following a failed job ends without job_failed');
        } catch (AgentJobFailed $e) {
            $this->assertSame('Out of patience', $e->getMessage());
        }
        $this->assertCount(2, $lines);
        $this->assertStringEndsWith('] Sleeping 5 seconds', $lines[0]);
    }

    /** follow() resumes from an offset, as the Jobs screen does after a reload. */
    public function testFollowFromOffset(): void
    {
        $fake = (new FakeAgentClient)->scriptJob('e2e.sleep', ['succeeded'], log: ['one', 'two', 'three']);
        $job = $fake->submit(new E2eSleep(5, 'shop'));
        $all = [];
        $fake->follow($job->id, 0, function (string $line, int $offset) use (&$all): void {
            $all[$offset] = $line;
        });

        $rest = [];
        $fake->follow($job->id, array_keys($all)[1], function (string $line, int $offset) use (&$rest): void {
            $rest[$offset] = $line;
        });

        $this->assertSame(array_slice($all, 1, null, true), $rest);
    }

    public function testRespondAnswersInOrderThenRepeatsTheLast(): void
    {
        $fake = (new FakeAgentClient)->respond('agent.version', ['version' => '1'], ['version' => '2']);

        $this->assertSame(['1', '2', '2'], array_map(fn () => $fake->call(new AgentVersion)->data['version'], range(1, 3)));
    }

    public function testSimulatesValidationAndBusy(): void
    {
        $fake = new FakeAgentClient;
        try {
            $fake->submit(new E2eSleep(500, 'Not A Name'));
            $this->fail('no validation_failed');
        } catch (AgentValidationFailed $e) {
            $this->assertEqualsCanonicalizing(['seconds', 'subject'], array_keys($e->fields()));
        }

        $fake->submit(new E2eSleep(5, 'one'));
        $fake->submit(new E2eSleep(5, 'two'));
        $this->expectException(AgentBusy::class); // the global cap is 2 unfinished jobs (§5.7)
        $fake->submit(new E2eSleep(5, 'three'));
    }

    public function testRecordsEveryCall(): void
    {
        $fake = new FakeAgentClient;
        $job = $fake->submit(new E2eSleep(5, 'shop'));
        $fake->status($job->id);
        $fake->call(new SystemStatus);

        $this->assertSame([
            ['method' => 'submit', 'command' => 'e2e.sleep', 'params' => ['seconds' => 5, 'subject' => 'shop']],
            ['method' => 'status', 'command' => 'job.status', 'params' => ['job' => $job->id]],
            ['method' => 'call', 'command' => 'system.status', 'params' => []],
        ], $fake->calls());
    }

    /** demo() gives Herd a server worth looking at: versions, warnings and counts. */
    public function testDemoLooksLikeABusyServer(): void
    {
        $status = FakeAgentClient::demo()->call(new SystemStatus)->data;

        $this->assertGreaterThanOrEqual(2, count($status['php']['installed']));
        $this->assertTrue($status['reboot_required']);
        $this->assertGreaterThan(0, $status['updates']['security']);
    }
}
