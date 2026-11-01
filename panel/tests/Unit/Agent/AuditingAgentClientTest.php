<?php

namespace Tests\Unit\Agent;

use App\Agent\AuditingAgentClient;
use App\Agent\Commands\AgentVersion;
use App\Agent\Commands\E2eSleep;
use App\Agent\Commands\JobCancel;
use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentForbidden;
use App\Agent\Exceptions\AgentInvalidRequest;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\Exceptions\AgentUnknownCommand;
use App\Agent\Exceptions\AgentUnsupportedProtocol;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\FakeAgentClient;
use App\Models\AgentAction;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Schema;
use Tests\Support\SecretCommand;
use Tests\TestCase;

/** The panel's copy of what the agent audits (D9, §7.6): changes, secrets and rejections, never routine reads. */
final class AuditingAgentClientTest extends TestCase
{
    use RefreshDatabase;

    public function testSubmitLogged(): void
    {
        $user = User::factory()->create();
        $this->actingAs($user);

        (new AuditingAgentClient(new FakeAgentClient))->submit(new E2eSleep(5, 'shop'));

        $row = AgentAction::sole();
        $this->assertSame('e2e.sleep', $row->command);
        $this->assertSame($user->id, $row->actor_id);
        $this->assertSame(['seconds' => 5, 'subject' => 'shop'], $row->params);
        $this->assertSame('accepted', $row->outcome);
        $this->assertGreaterThanOrEqual(0, $row->duration_ms);
        $this->assertNotNull($row->created_at);
    }

    /** A refused submit is logged with its code and still reaches the caller. */
    public function testRefusedSubmitLogged(): void
    {
        foreach ([
            'error:busy' => new AgentBusy('The subject already has a running job'),
            'error:unavailable' => new AgentUnavailable('Cannot reach the agent'),
        ] as $outcome => $answer) {
            $fake = (new FakeAgentClient)->respond('e2e.sleep', $answer);

            try {
                (new AuditingAgentClient($fake))->submit(new E2eSleep(5, 'shop'));
                $this->fail("{$outcome}: no exception");
            } catch (AgentException $e) {
                $this->assertSame($answer, $e);
            }

            $this->assertSame($outcome, AgentAction::query()->latest('id')->value('outcome'));
        }
        $this->assertNull(AgentAction::query()->first()->actor_id, 'outside a login the actor is the system');
    }

    public function testReadNotLogged(): void
    {
        $fake = new FakeAgentClient;
        $job = $fake->submit(new E2eSleep(5, 'shop'));
        $client = new AuditingAgentClient($fake);

        $client->call(new AgentVersion);
        $client->call(new SystemStatus);
        $client->status($job->id);
        $client->follow($job->id, 0, fn () => null);
        try {
            $client->status('01JBX7Q3A1B2C3D4E5F6G7H8J9');
        } catch (AgentNotFound) {
            // not a rejection: the request was fine and the job doesn't exist
        }

        $this->assertSame(0, AgentAction::count());
    }

    /** Requests the agent rejects are logged, reads included. */
    public function testRejectionLogged(): void
    {
        foreach ([
            new AgentInvalidRequest('Invalid request'),
            new AgentUnsupportedProtocol('Protocol version 2 is not supported'),
            new AgentUnknownCommand('Unknown command "system.status"'),
            new AgentValidationFailed('Invalid parameters', ['params' => 'unknown field']),
            new AgentForbidden('Forbidden'),
        ] as $rejection) {
            $client = new AuditingAgentClient((new FakeAgentClient)->respond('system.status', $rejection));
            try {
                $client->call(new SystemStatus);
                $this->fail("no exception for {$rejection->agentCode()}");
            } catch (AgentException $e) {
                $this->assertSame($rejection, $e);
            }

            $row = AgentAction::query()->latest('id')->first();
            $this->assertSame('system.status', $row->command);
            $this->assertSame('error:'.$rejection->agentCode(), $row->outcome);
        }
        $this->assertSame(5, AgentAction::count());
    }

    public function testCancelLogged(): void
    {
        $fake = new FakeAgentClient;
        $job = $fake->submit(new E2eSleep(30, 'shop'));

        (new AuditingAgentClient($fake))->call(new JobCancel($job->id));

        $row = AgentAction::sole();
        $this->assertSame('job.cancel', $row->command);
        $this->assertSame(['job' => $job->id], $row->params);
        $this->assertSame('ok', $row->outcome);
    }

    public function testSecretsRedacted(): void
    {
        (new AuditingAgentClient(new FakeAgentClient))->submit(new SecretCommand);

        $this->assertSame([
            'name' => 'shop',
            'token' => '[redacted]',
            'users' => [
                ['username' => 'ann', 'password_hash' => '[redacted]'],
                ['username' => 'bob', 'password_hash' => '[redacted]'],
            ],
        ], AgentAction::sole()->params);
    }

    /** A log row that can't be written never hides what the agent answered. */
    public function testFailedRowWriteKeepsTheAgentsAnswer(): void
    {
        Schema::drop('agent_actions');

        $job = (new AuditingAgentClient(new FakeAgentClient))->submit(new E2eSleep(5, 'shop'));
        $this->assertNotSame('', $job->id);

        $this->expectException(AgentBusy::class);
        (new AuditingAgentClient((new FakeAgentClient)->respond('e2e.sleep', new AgentBusy('busy'))))->submit(new E2eSleep(5, 'shop'));
    }
}
