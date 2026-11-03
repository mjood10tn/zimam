<?php

namespace Tests\Contract;

use App\Agent\AgentClient;
use App\Agent\AgentJobStatus;
use App\Agent\Commands\AgentVersion;
use App\Agent\Commands\E2eSleep;
use App\Agent\Commands\JobCancel;
use App\Agent\Commands\SiteProvision;
use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentValidationFailed;
use Illuminate\Support\Str;
use Tests\TestCase;

/**
 * The contract scenarios (design §6), run against every client: the fake on
 * every CI run, the socket client against a real agent in the end-to-end
 * runs. The job scenarios provision sites; cancelling uses the test-only
 * e2e.sleep (D22), since a site provisions in about a second and cancelling
 * one would race its end. Each scenario uses its own subject and ends what
 * it started, since the real agent allows two unfinished jobs at a time.
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
            $this->client->submit(new SiteProvision('Bad_Name', 'contract.example.com', 'static'));
            $this->fail('site.provision accepted the site name "Bad_Name"');
        } catch (AgentValidationFailed $e) {
            $this->assertArrayHasKey('name', $e->fields());
            $this->assertArrayNotHasKey('domain', $e->fields());
        }
    }

    public function testNotFoundForUnknownJob(): void
    {
        $this->expectException(AgentNotFound::class);
        $this->client->status(strtoupper((string) Str::ulid()));
    }

    /**
     * Provision → status → follow → result, for a PHP site on the server's
     * default version, so a real agent writes a pool and reloads PHP-FPM as
     * well as Nginx.
     */
    public function testJobLifecycle(): void
    {
        $php = $this->client->call(new SystemStatus)->data['php']['default'];
        $job = $this->client->submit(new SiteProvision('contract-life', 'contract-life.example.com', 'php', $php));

        $this->assertContains($this->client->status($job->id)->status, ['queued', 'running', 'succeeded']);
        $lines = 0;
        $final = $this->client->follow($job->id, 0, function () use (&$lines): void {
            $lines++;
        });

        $this->assertSame('succeeded', $final->status, $final->message);
        $this->assertGreaterThan(0, $lines);
        $this->assertSame($job->id, $final->job);
        $this->assertSame('contract-life', $final->result['name']);
        $this->assertGreaterThanOrEqual(1, $final->result['template_version']);
    }

    /**
     * A second job for the same site is busy. The first is followed to its
     * end rather than cancelled, so no half-made site is left behind.
     */
    public function testBusyForSameSubject(): void
    {
        $command = new SiteProvision('contract-busy', 'contract-busy.example.com', 'static');
        $first = $this->client->submit($command);
        try {
            $this->expectException(AgentBusy::class);
            $this->client->submit($command);
        } finally {
            $this->assertSame('succeeded', $this->client->follow($first->id, 0, function (): void {})->status);
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
