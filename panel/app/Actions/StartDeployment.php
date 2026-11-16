<?php

namespace App\Actions;

use App\Agent\Commands\SiteDeploy;
use App\Models\Deployment;
use App\Models\Site;
use App\Models\User;
use LogicException;

/**
 * Starts a deploy of a site from its repository settings (R02-D5): Deploy
 * now, a GitHub push or the deploy URL. It goes through the D8 path, so a
 * busy site's deploy waits and retries (R02-D12), and the deployments row
 * follows its job from there. The agent's refusals are rethrown.
 */
final class StartDeployment
{
    public function __construct(private readonly SubmitAgentCommand $submit) {}

    public function run(Site $site, string $trigger, ?string $commit = null, ?User $by = null): Deployment
    {
        $repository = $site->repository;
        if ($repository === null || ! $repository->isConnected()) {
            throw new LogicException("Site {$site->name} has no connected repository");
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
}
