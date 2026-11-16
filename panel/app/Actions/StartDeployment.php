<?php

namespace App\Actions;

use App\Agent\Commands\SiteDeploy;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use App\Models\Deployment;
use App\Models\Site;
use App\Models\User;
use Illuminate\Database\Eloquent\Relations\MorphMany;
use LogicException;

/**
 * Starts a deploy of a site from its repository settings (R02-D5): Deploy
 * now, a GitHub push or the deploy URL. It goes through the D8 path, so a
 * busy site's deploy waits and retries (R02-D12), and the deployments row
 * follows its job from there. The agent's refusals are rethrown.
 *
 * At most one deploy of a site waits. Waiting jobs retry on their own
 * clocks, so two of them would start in no fixed order and an older push
 * could go live after a newer one. A deploy of the branch's head that finds
 * one already waiting is that one. Any other new deploy takes the place of
 * the older waiting ones, which end as superseded.
 */
final class StartDeployment
{
    public function __construct(private readonly SubmitAgentCommand $submit, private readonly JobOutcomes $outcomes) {}

    public function run(Site $site, string $trigger, ?string $commit = null, ?User $by = null): Deployment
    {
        $repository = $site->repository;
        if ($repository === null || ! $repository->isConnected()) {
            throw new LogicException("Site {$site->name} has no connected repository");
        }
        // The head, whatever it is when the waiting deploy starts, is what
        // this one would deploy too.
        if ($commit === null && ($waiting = $this->waitingHeadDeploy($site)) !== null) {
            return $waiting;
        }
        $command = new SiteDeploy(
            site: $site->name,
            trigger: $trigger,
            composer: $site->runsPhp() && $repository->run_composer,
            keepReleases: $repository->keep_releases,
            script: $repository->deploy_script,
            commit: $commit,
        );
        $job = $this->submit->run($command, $site);
        // Only now: a submit the agent refused leaves the older ones be.
        foreach ($this->waitingJobs($site)->where('id', '<', $job->id)->get() as $older) {
            $this->outcomes->cancelWaiting($older, 'superseded', __('zimam.deployments.deploy.superseded'));
        }
        $deployment = Deployment::create([
            'site_id' => $site->id,
            'agent_job_id' => $job->id,
            'trigger' => $trigger,
            'commit' => $commit,
            'status' => $job->status,
            'created_by' => $by?->id,
        ]);
        // The job may have moved on already, with a synchronous queue.
        Deployment::follow($job->refresh());

        return $deployment->refresh();
    }

    /** @return MorphMany<AgentJob, Site> */
    private function waitingJobs(Site $site)
    {
        return $site->jobs()->where('command', 'site.deploy')->where('status', AgentJob::WAITING);
    }

    private function waitingHeadDeploy(Site $site): ?Deployment
    {
        return Deployment::query()
            ->whereIn('agent_job_id', $this->waitingJobs($site)->select('id'))
            ->whereNull('commit')
            ->latest('id')
            ->first();
    }
}
