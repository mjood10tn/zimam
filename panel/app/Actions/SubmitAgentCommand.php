<?php

namespace App\Actions;

use App\Agent\AgentClient;
use App\Agent\AgentCommand;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentUnavailable;
use App\Jobs\SubmitAgentJob;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use Filament\Notifications\Notification;
use Filament\Support\Exceptions\Halt;
use Illuminate\Database\Eloquent\Model;

/**
 * Submits a job command synchronously first, so validation errors reach the
 * form (D8). AgentValidationFailed and the other refusals are rethrown.
 */
final class SubmitAgentCommand
{
    public function __construct(private readonly AgentClient $agent) {}

    /**
     * With $waitWhenBusy false a busy answer is rethrown instead of queued
     * for a retry: the queue would hold the command, and a one-time password
     * must not wait in a queue table, even encrypted.
     *
     * @throws Halt when the agent can't be reached, after telling the user if there is one
     */
    public function run(AgentCommand $command, ?Model $related = null, bool $waitWhenBusy = true): AgentJob
    {
        try {
            $accepted = $this->agent->submit($command);
        } catch (AgentBusy $busy) {
            if (! $waitWhenBusy) {
                throw $busy;
            }
            $job = $this->record($command, $related, AgentJob::WAITING);
            SubmitAgentJob::dispatch($job, $command)->delay(SubmitAgentJob::BACKOFF[0]);

            return $job;
        } catch (AgentUnavailable) {
            // A hook request has no session, so no one to tell: its
            // controller answers the caller itself.
            if (request()->hasSession()) {
                Notification::make()
                    ->title(__('zimam.agent.unavailable.title'))
                    ->body(__('zimam.agent.unavailable.body'))
                    ->danger()
                    ->send();
            }

            throw new Halt;
        }

        $job = $this->record($command, $related, AgentJob::QUEUED, $accepted->id);
        TrackAgentJob::dispatch($job);

        return $job;
    }

    private function record(AgentCommand $command, ?Model $related, string $status, ?string $agentJobId = null): AgentJob
    {
        $job = new AgentJob([
            'agent_job_id' => $agentJobId,
            'command' => $command->name(),
            'status' => $status,
            'created_by' => auth()->id(),
            'runtime_max_seconds' => $command->runtimeMaxSeconds(),
            'queued_at' => $agentJobId === null ? null : now(),
            'params' => $command->redactedParams(),
        ]);
        $job->related()->associate($related);
        $job->save();

        return $job;
    }
}
