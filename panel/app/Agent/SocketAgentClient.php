<?php

namespace App\Agent;

use App\Agent\Commands\JobFollow;
use App\Agent\Commands\JobStatus;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use Closure;
use Illuminate\Support\Str;
use JsonException;
use LogicException;

/**
 * Talks to the agent over its socket with protocol version 1 (§5.2): one
 * request line per connection, answered by JSON lines.
 */
final class SocketAgentClient implements AgentClient
{
    public const PROTOCOL = 1;

    /** §5.2's limit on a line. */
    public const MAX_LINE = 1024 * 1024;

    /**
     * @param  int  $readTimeout  seconds a direct command may take to answer (§5.2: 30)
     */
    public function __construct(
        private readonly AgentTransport $transport,
        private readonly int $readTimeout = 30,
    ) {}

    public function call(AgentCommand $command): AgentResult
    {
        if ($command->kind() === AgentCommandKind::Job) {
            throw new LogicException("{$command->name()} is a job: submit it");
        }
        $reply = $this->send($command, $this->readTimeout);
        try {
            return new AgentResult($this->expect($reply, 'result')['data']);
        } finally {
            fclose($reply);
        }
    }

    public function submit(AgentCommand $command): AgentJob
    {
        if ($command->kind() !== AgentCommandKind::Job) {
            throw new LogicException("{$command->name()} isn't a job: call it");
        }
        $reply = $this->send($command, $this->readTimeout);
        try {
            return new AgentJob($this->expect($reply, 'accepted')['job']);
        } finally {
            fclose($reply);
        }
    }

    public function status(string $jobId): AgentJobStatus
    {
        return AgentJobStatus::fromArray($this->call(new JobStatus($jobId))->data);
    }

    /**
     * Without a read timeout: a job may run for many minutes between two log
     * lines. A failed job ends with a job_failed error (AgentJobFailed).
     *
     * @param  Closure(string $line, int $offset): void  $onLine
     */
    public function follow(string $jobId, int $fromOffset, Closure $onLine): AgentJobStatus
    {
        $reply = $this->send(new JobFollow($jobId, $fromOffset), null);
        try {
            while (($line = $this->readLine($reply))['type'] === 'log') {
                $onLine((string) $line['line'], (int) $line['offset']);
            }

            return AgentJobStatus::fromArray($this->terminal($line, 'result')['data']);
        } finally {
            fclose($reply);
        }
    }

    /** @return resource the reply stream */
    private function send(AgentCommand $command, ?int $readTimeout)
    {
        $request = [
            'v' => self::PROTOCOL,
            'id' => strtoupper((string) Str::ulid()),
            'command' => $command->name(),
            'actor' => auth()->check() ? 'user:'.auth()->id() : 'system',
            'params' => $command->jsonParams(),
        ];

        return $this->transport->send(json_encode($request, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES)."\n", $readTimeout);
    }

    /**
     * Reads the reply's one line and checks its type.
     *
     * @param  resource  $reply
     * @return array<string, mixed>
     */
    private function expect($reply, string $type): array
    {
        return $this->terminal($this->readLine($reply), $type);
    }

    /**
     * An error line becomes its exception; any other line must be of $type.
     *
     * @param  array<string, mixed>  $line
     * @return array<string, mixed>
     */
    private function terminal(array $line, string $type): array
    {
        if ($line['type'] === 'error') {
            throw AgentException::fromErrorLine($line);
        }
        $ok = match ($type) {
            'result' => ($line['ok'] ?? null) === true && is_array($line['data'] ?? null),
            'accepted' => is_string($line['job'] ?? null),
        };
        if ($line['type'] !== $type || ! $ok) {
            throw new AgentException("The agent answered with an unexpected {$line['type']} line where a {$type} line belongs");
        }

        return $line;
    }

    /**
     * Reads one JSON line of at most MAX_LINE bytes.
     *
     * @param  resource  $reply
     * @return array<string, mixed>
     */
    private function readLine($reply): array
    {
        // fgets reads at most length - 1 bytes: a line that fits comes with
        // its newline, and a longer one stops at MAX_LINE + 1 bytes without.
        $raw = fgets($reply, self::MAX_LINE + 2);
        if ($raw === false) {
            if (stream_get_meta_data($reply)['timed_out']) {
                throw new AgentUnavailable('The agent stopped answering');
            }
            throw new AgentUnavailable('The agent closed the connection without an answer');
        }
        if (! str_ends_with($raw, "\n")) {
            if (strlen($raw) > self::MAX_LINE) {
                throw new AgentException('The agent sent a line over 1 MiB');
            }
            throw new AgentUnavailable(stream_get_meta_data($reply)['timed_out']
                ? 'The agent stopped answering'
                : 'The agent closed the connection in the middle of a line');
        }
        try {
            $line = json_decode($raw, true, 512, JSON_THROW_ON_ERROR);
        } catch (JsonException) {
            throw new AgentException('The agent sent a line that is not JSON');
        }
        if (! is_array($line) || ! is_string($line['type'] ?? null)) {
            throw new AgentException('The agent sent a line without a type');
        }

        return $line;
    }
}
