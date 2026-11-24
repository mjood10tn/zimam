<?php

namespace App\Agent\Outcomes;

use App\Agent\AgentJobStatus;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\GuardedChange;
use Carbon\CarbonImmutable;

/**
 * A succeeded firewall.rules or ssh.settings job has applied its change and
 * started the agent's revert timer (R06-D1): the panel records the change,
 * and the Server security page asks the owner to keep it. Nothing else moves
 * until the owner keeps it, so a reverted change leaves the panel's copy as
 * it was. cloudflare.mode records one through ServerSecurityOutcomes.
 */
final class GuardedChangeOutcomes
{
    public static function register(JobOutcomes $outcomes): void
    {
        foreach (['firewall.rules', 'ssh.settings'] as $command) {
            $outcomes->register($command, function (AgentJob $job, AgentJobStatus $status): void {
                if ($status->status === AgentJob::SUCCEEDED) {
                    self::record($job, $status);
                }
            });
        }
    }

    /** Records the change a succeeded job's result names; a result without one started none. */
    public static function record(AgentJob $job, AgentJobStatus $status): void
    {
        if (! is_array($status->result) || ! isset($status->result['change'], $status->result['deadline'])) {
            return;
        }
        GuardedChange::create([
            'agent_change_id' => $status->result['change'],
            'kind' => GuardedChange::COMMANDS[$job->command],
            'deadline' => CarbonImmutable::parse($status->result['deadline']),
            'status' => GuardedChange::PENDING,
            'params' => $job->params,
            'created_by' => $job->created_by,
        ]);
    }
}
