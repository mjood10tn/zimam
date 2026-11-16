<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Connects a site to a GitHub repository and branch (R02-D4). The agent
 * makes the site's deploy key as the site user, or keeps the one it has
 * unless rotate_key is set, and answers with the public half only.
 */
final class SiteRepo extends AgentCommand
{
    public function __construct(
        public readonly string $site,
        public readonly string $repository,
        public readonly string $branch,
        public readonly bool $rotateKey = false,
    ) {}

    public function name(): string
    {
        return 'site.repo';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        $params = ['name' => $this->site, 'repository' => $this->repository, 'branch' => $this->branch];
        if ($this->rotateKey) {
            $params['rotate_key'] = true;
        }

        return $params;
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 600;
    }
}
