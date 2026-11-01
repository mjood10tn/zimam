<?php

namespace App\Agent;

use App\Agent\Commands\JobFollow;
use App\Agent\Commands\JobStatus;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use App\Models\AgentAction;
use Closure;
use Throwable;

/**
 * Keeps the panel's own copy of what the agent audits (D9, §7.6): one
 * agent_actions row for every submit(), every job.cancel and every request
 * the agent rejects, with params redacted by the command's secret fields.
 * Routine reads are never logged.
 */
final class AuditingAgentClient implements AgentClient
{
    /** The error codes that mean the agent rejected the request itself (§5.8). */
    private const REJECTIONS = ['invalid_request', 'unsupported_protocol', 'unknown_command', 'validation_failed', 'forbidden'];

    public function __construct(private readonly AgentClient $inner) {}

    public function call(AgentCommand $command): AgentResult
    {
        return $this->logged($command, $command->name() === 'job.cancel' ? 'ok' : null, fn () => $this->inner->call($command));
    }

    public function submit(AgentCommand $command): AgentJob
    {
        return $this->logged($command, 'accepted', fn () => $this->inner->submit($command));
    }

    public function status(string $jobId): AgentJobStatus
    {
        return $this->logged(new JobStatus($jobId), null, fn () => $this->inner->status($jobId));
    }

    public function follow(string $jobId, int $fromOffset, Closure $onLine): AgentJobStatus
    {
        return $this->logged(new JobFollow($jobId, $fromOffset), null, fn () => $this->inner->follow($jobId, $fromOffset, $onLine));
    }

    /**
     * Runs $send and logs it: always when $success names its outcome,
     * otherwise only when the agent rejects the request.
     *
     * @template T
     *
     * @param  Closure(): T  $send
     * @return T
     */
    private function logged(AgentCommand $command, ?string $success, Closure $send): mixed
    {
        $start = hrtime(true);
        try {
            $answer = $send();
        } catch (AgentException $e) {
            if ($success !== null || in_array($e->agentCode(), self::REJECTIONS, true)) {
                $this->record($command, 'error:'.($e->agentCode() ?? ($e instanceof AgentUnavailable ? 'unavailable' : 'bad_reply')), $start);
            }
            throw $e;
        }
        if ($success !== null) {
            $this->record($command, $success, $start);
        }

        return $answer;
    }

    /** A row that can't be written is reported, never put in place of the agent's answer. */
    private function record(AgentCommand $command, string $outcome, int $start): void
    {
        try {
            AgentAction::create([
                'command' => $command->name(),
                'actor_id' => auth()->id(),
                'params' => $command->redactedParams(),
                'outcome' => $outcome,
                'duration_ms' => intdiv(hrtime(true) - $start, 1_000_000),
            ]);
        } catch (Throwable $e) {
            report($e);
        }
    }
}
