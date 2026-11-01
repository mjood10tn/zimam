<?php

namespace Tests\Unit\Agent;

use App\Agent\Commands\E2eSleep;
use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentForbidden;
use App\Agent\Exceptions\AgentInternal;
use App\Agent\Exceptions\AgentInvalidRequest;
use App\Agent\Exceptions\AgentJobFailed;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\Exceptions\AgentUnknownCommand;
use App\Agent\Exceptions\AgentUnsupportedProtocol;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\SocketAgentClient;
use App\Agent\UnixSocketTransport;
use App\Models\User;
use Opis\JsonSchema\Validator;
use PHPUnit\Framework\Attributes\DataProvider;
use Tests\Support\MemoryTransport;
use Tests\TestCase;

final class SocketAgentClientTest extends TestCase
{
    private const JOB = '01JBX7Q3A1B2C3D4E5F6G7H8J9';

    /** @return array<string, array{string, class-string<AgentException>}> */
    public static function errorCodes(): array
    {
        return [
            'invalid_request' => ['invalid_request', AgentInvalidRequest::class],
            'unsupported_protocol' => ['unsupported_protocol', AgentUnsupportedProtocol::class],
            'unknown_command' => ['unknown_command', AgentUnknownCommand::class],
            'validation_failed' => ['validation_failed', AgentValidationFailed::class],
            'forbidden' => ['forbidden', AgentForbidden::class],
            'busy' => ['busy', AgentBusy::class],
            'not_found' => ['not_found', AgentNotFound::class],
            'conflict' => ['conflict', AgentConflict::class],
            'job_failed' => ['job_failed', AgentJobFailed::class],
            'internal' => ['internal', AgentInternal::class],
        ];
    }

    /** Each §5.2 error code reaches the caller as its own exception, with the agent's message. */
    #[DataProvider('errorCodes')]
    public function testErrorCodeMapsToItsException(string $code, string $class): void
    {
        $client = new SocketAgentClient(MemoryTransport::replying(['type' => 'error', 'code' => $code, 'message' => "The agent said {$code}"]));

        try {
            $client->call(new SystemStatus);
            $this->fail("no exception for {$code}");
        } catch (AgentException $e) {
            $this->assertSame($class, $e::class);
            $this->assertSame($code, $e->agentCode());
            $this->assertSame("The agent said {$code}", $e->getMessage());
        }
    }

    public function testValidationFailedExposesFields(): void
    {
        $client = new SocketAgentClient(MemoryTransport::replying(['type' => 'error', 'code' => 'validation_failed', 'message' => 'Invalid parameters', 'fields' => ['job' => 'must be a ULID']]));

        try {
            $client->status('bad');
            $this->fail('no exception');
        } catch (AgentValidationFailed $e) {
            $this->assertSame(['job' => 'must be a ULID'], $e->fields());
        }
    }

    /** One request per connection: a single JSON line (§5.2), from "system" outside a login. */
    public function testWritesOneLine(): void
    {
        $transport = MemoryTransport::replying(['type' => 'result', 'ok' => true, 'data' => ['version' => '0.1.0']]);
        (new SocketAgentClient($transport))->call(new SystemStatus);

        $this->assertCount(1, $transport->lines);
        $line = $transport->lines[0];
        $this->assertStringEndsWith("\n", $line);
        $this->assertSame(1, substr_count($line, "\n"));
        $this->assertStringContainsString('"params":{}', $line, 'empty params must be a JSON object');

        $request = json_decode($line, false, 512, JSON_THROW_ON_ERROR);
        $this->assertSame(1, $request->v);
        $this->assertMatchesRegularExpression('/^[0-9A-HJKMNP-TV-Z]{26}\z/', $request->id);
        $this->assertSame('system.status', $request->command);
        $this->assertSame('system', $request->actor);

        $envelope = json_decode(file_get_contents(base_path('../contract/schema/envelope.json')));
        $result = (new Validator)->validate($request, $envelope->{'$defs'}->request);
        $this->assertTrue($result->isValid(), 'the request line breaks envelope.json#/$defs/request');
    }

    public function testActorIsTheLoggedInUser(): void
    {
        $user = new User;
        $user->id = 7;
        $transport = MemoryTransport::replying(['type' => 'result', 'ok' => true, 'data' => []]);
        $this->actingAs($user);

        (new SocketAgentClient($transport))->call(new SystemStatus);

        $this->assertSame('user:7', json_decode($transport->lines[0])->actor);
    }

    /** Direct commands wait 30 seconds for a reply; job.follow waits as long as the job runs (§5.2). */
    public function testFollowHasNoReadTimeout(): void
    {
        $transport = MemoryTransport::replying(['type' => 'result', 'ok' => true, 'data' => self::jobStatus('succeeded')]);
        $client = new SocketAgentClient($transport);

        $client->call(new SystemStatus);
        $client->follow(self::JOB, 0, fn () => null);

        $this->assertSame([30, null], $transport->timeouts);
    }

    public function testSubmitReturnsTheAcceptedJob(): void
    {
        $client = new SocketAgentClient(MemoryTransport::replying(['type' => 'accepted', 'job' => self::JOB]));

        $this->assertSame(self::JOB, $client->submit(new E2eSleep(5, 'shop'))->id);
    }

    public function testFollowCallsClosurePerLine(): void
    {
        $client = new SocketAgentClient(MemoryTransport::replying(
            ['type' => 'log', 'job' => self::JOB, 'offset' => 0, 'line' => '[2026-11-01T10:00:00Z] Sleeping 2 seconds'],
            ['type' => 'log', 'job' => self::JOB, 'offset' => 42, 'line' => '[2026-11-01T10:00:02Z] Done'],
            ['type' => 'result', 'ok' => true, 'data' => self::jobStatus('succeeded')],
        ));
        $seen = [];

        $status = $client->follow(self::JOB, 0, function (string $line, int $offset) use (&$seen): void {
            $seen[] = [$offset, $line];
        });

        $this->assertSame([
            [0, '[2026-11-01T10:00:00Z] Sleeping 2 seconds'],
            [42, '[2026-11-01T10:00:02Z] Done'],
        ], $seen);
        $this->assertSame('succeeded', $status->status);
        $this->assertTrue($status->isFinished());
    }

    /** A failed job ends its stream with a job_failed error carrying the job's message. */
    public function testFollowOfFailedJobThrowsJobFailed(): void
    {
        $client = new SocketAgentClient(MemoryTransport::replying(
            ['type' => 'log', 'job' => self::JOB, 'offset' => 0, 'line' => '[2026-11-01T10:00:00Z] Failed: no space left'],
            ['type' => 'error', 'code' => 'job_failed', 'message' => 'no space left'],
        ));

        $this->expectException(AgentJobFailed::class);
        $this->expectExceptionMessage('no space left');
        $client->follow(self::JOB, 0, fn () => null);
    }

    /** The agent's times are Go's RFC 3339, with up to nine fractional digits. */
    public function testStatusReadsGoTimestamps(): void
    {
        $data = self::jobStatus('succeeded');
        $data['started_at'] = '2026-11-01T10:00:00Z';
        $data['finished_at'] = '2026-11-01T10:00:02.123456789Z';
        $status = (new SocketAgentClient(MemoryTransport::replying(['type' => 'result', 'ok' => true, 'data' => $data])))->status(self::JOB);

        $this->assertSame('2026-11-01T10:00:00.000000Z', $status->startedAt?->format('Y-m-d\TH:i:s.u\Z'));
        $this->assertSame('2026-11-01T10:00:02.123456Z', $status->finishedAt?->format('Y-m-d\TH:i:s.u\Z'));
        $this->assertSame(512, $status->logSize);
    }

    /** §5.2 caps a line at 1 MiB; a longer reply line is refused, not buffered. */
    public function testRejectsOversizedResponseLine(): void
    {
        $line = '{"type":"result","ok":true,"data":{"pad":"'.str_repeat('x', 1024 * 1024).'"}}'."\n";
        $client = new SocketAgentClient(new MemoryTransport($line));

        $this->expectException(AgentException::class);
        $this->expectExceptionMessage('1 MiB');
        $client->call(new SystemStatus);
    }

    /** The limit is exact: a line of 1 MiB passes, one byte more doesn't. */
    public function testLineLimitIsExactlyOneMiB(): void
    {
        $line = fn (int $size): string => str_pad('{"type":"result","ok":true,"data":{"pad":"', $size - 3, 'x').'"}}';
        $this->assertSame(SocketAgentClient::MAX_LINE, strlen($line(SocketAgentClient::MAX_LINE)));

        $result = (new SocketAgentClient(new MemoryTransport($line(SocketAgentClient::MAX_LINE)."\n")))->call(new SystemStatus);
        $this->assertArrayHasKey('pad', $result->data);

        $this->expectExceptionMessage('1 MiB');
        (new SocketAgentClient(new MemoryTransport($line(SocketAgentClient::MAX_LINE + 1)."\n")))->call(new SystemStatus);
    }

    /** A reply that stops before its newline means the connection dropped. */
    public function testReplyCutShortIsUnavailable(): void
    {
        foreach (['', '{"type":"result","ok":true,"data":{}}'] as $reply) {
            try {
                (new SocketAgentClient(new MemoryTransport($reply)))->call(new SystemStatus);
                $this->fail('no exception for '.json_encode($reply));
            } catch (AgentUnavailable) {
                $this->addToAssertionCount(1);
            }
        }
    }

    public function testInvalidReplyIsAnAgentException(): void
    {
        foreach (["not json\n", '{"type":"accepted","job":"'.self::JOB."\"}\n"] as $reply) {
            try {
                (new SocketAgentClient(new MemoryTransport($reply)))->call(new SystemStatus);
                $this->fail('no exception for '.json_encode($reply));
            } catch (AgentException $e) {
                $this->assertNull($e->agentCode());
                $this->assertNotInstanceOf(AgentUnavailable::class, $e);
            }
        }
    }

    public function testConnectFailureIsUnavailable(): void
    {
        $client = new SocketAgentClient(new UnixSocketTransport(sys_get_temp_dir().'/zimam-no-such-agent-'.bin2hex(random_bytes(4)).'.sock'));

        $this->expectException(AgentUnavailable::class);
        $client->call(new SystemStatus);
    }

    /** An agent that accepts the connection but never answers is unavailable once the read timeout passes. */
    public function testReadTimeoutIsUnavailable(): void
    {
        if (! in_array('unix', stream_get_transports(), true)) {
            $this->markTestSkipped('this PHP has no Unix sockets');
        }
        $path = sys_get_temp_dir().'/zimam-silent-'.bin2hex(random_bytes(4)).'.sock';
        $server = stream_socket_server('unix://'.$path);
        $this->assertNotFalse($server);

        try {
            $client = new SocketAgentClient(new UnixSocketTransport($path), readTimeout: 1);
            $this->expectException(AgentUnavailable::class);
            $client->call(new SystemStatus);
        } finally {
            fclose($server);
            @unlink($path);
        }
    }

    /** @return array<string, mixed> */
    private static function jobStatus(string $status): array
    {
        return [
            'job' => self::JOB,
            'command' => 'e2e.sleep',
            'subject' => 'e2e:shop',
            'status' => $status,
            'error_code' => null,
            'message' => '',
            'result' => null,
            'started_at' => '2026-11-01T10:00:00Z',
            'finished_at' => '2026-11-01T10:00:02Z',
            'log_size' => 512,
        ];
    }
}
