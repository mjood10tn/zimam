<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Builds a release from the site's branch and makes it live (R02-D5). The
 * script is the owner's, run by the site user's bash (R02-D2); it is not a
 * secret. keep_releases is always sent, so the agent prunes by the panel's
 * setting. An empty script and no commit are left out.
 */
final class SiteDeploy extends AgentCommand
{
    public function __construct(
        public readonly string $site,
        public readonly string $trigger,
        public readonly bool $composer,
        public readonly int $keepReleases,
        public readonly string $script = '',
        public readonly ?string $commit = null,
    ) {}

    public function name(): string
    {
        return 'site.deploy';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        $params = ['name' => $this->site, 'composer' => $this->composer, 'trigger' => $this->trigger, 'keep_releases' => $this->keepReleases];
        if ($this->script !== '') {
            $params['script'] = $this->script;
        }
        if ($this->commit !== null) {
            $params['commit'] = $this->commit;
        }

        return $params;
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 1800;
    }
}
