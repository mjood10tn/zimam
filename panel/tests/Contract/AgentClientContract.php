<?php

namespace Tests\Contract;

use App\Agent\AgentClient;
use App\Agent\AgentJobStatus;
use App\Agent\Commands\AgentVersion;
use App\Agent\Commands\E2eSleep;
use App\Agent\Commands\JobCancel;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentValidationFailed;
use Illuminate\Support\Str;
use Tests\TestCase;

/**
 * The contract scenarios (design §6), run against every client: the fake on
 * every CI run, the socket client against a real agent in the end-to-end
 * runs. Until site.provision exists, the job scenarios use the test-only
 * e2e.sleep (D22). Each scenario uses its own subject and cancels what it
 * started, since the real agent allows two unfinished jobs at a time.
 */
abstract class AgentClientContract extends TestCase
{
    protected AgentClient $client;

    abstract protected function makeClient(): AgentClient;

    protected function setUp(): void
    {
        parent::setUp();
        $this->client = $this->makeClient();
    }

    public function testVersionHandshake(): void
    {
        $this->assertContains(1, $this->client->call(new AgentVersion)->data['protocols']);
    }

    public function testValidationErrorShape(): void
    {
        try {
            $this->client->status('bad');
            $this->fail('job.status accepted the job id "bad"');
        } catch (AgentValidationFailed $e) {
            $this->assertArrayHasKey('job', $e->fields());
        }
    }

    public function testNotFoundForUnknownJob(): void
    {
        $this->expectException(AgentNotFound::class);
        $this->client->status(strtoupper((string) Str::ulid()));
    }

    public function testJobLifecycle(): void
    {
        $job = $this->client->submit(new E2eSleep(2, 'contract-life'));
        try {
            $this->assertSame('running', $this->waitUntilRunning($job->id)->status);
            $lines = 0;
            $final = $this->client->follow($job->id, 0, function () use (&$lines): void {
                $lines++;
            });

            $this->assertSame('succeeded', $final->status);
            $this->assertGreaterThan(0, $lines);
            $this->assertSame($job->id, $final->job);
        } finally {
            $this->cancelQuietly($job->id);
        }
    }

    public function testBusyForSameSubject(): void
    {
        $first = $this->client->submit(new E2eSleep(30, 'contract-busy'));
        try {
            $this->expectException(AgentBusy::class);
            $this->client->submit(new E2eSleep(30, 'contract-busy'));
        } finally {
            $this->cancelQuietly($first->id);
        }
    }

    public function testCancelRunningJob(): void
    {
        $job = $this->client->submit(new E2eSleep(30, 'contract-cancel'));
        try {
            $this->waitUntilRunning($job->id);

            $this->assertSame('cancelled', $this->client->call(new JobCancel($job->id))->data['status']);
            $this->assertSame('cancelled', $this->client->status($job->id)->status);
        } finally {
            $this->cancelQuietly($job->id);
        }
    }

    private function waitUntilRunning(string $jobId): AgentJobStatus
    {
        $deadline = microtime(true) + 15;
        do {
            $status = $this->client->status($jobId);
            if ($status->status === 'running') {
                return $status;
            }
            $this->assertSame('queued', $status->status, 'the job ended before it ran');
            usleep(100_000);
        } while (microtime(true) < $deadline);
        $this->fail("job {$jobId} wasn't running within 15 seconds");
    }

    private function cancelQuietly(string $jobId): void
    {
        try {
            $this->client->call(new JobCancel($jobId));
        } catch (AgentException) {
            // already finished (conflict), or gone with the agent
        }
    }
}
