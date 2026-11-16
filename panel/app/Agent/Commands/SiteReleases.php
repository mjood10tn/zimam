<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * A site's kept releases, newest first, with the live one marked; plus the
 * ones the agent's state lists but the disk lacks (missing), and the
 * directories it doesn't list (unlisted). Only kept ones can be rolled back to.
 */
final class SiteReleases extends AgentCommand
{
    public function __construct(public readonly string $site) {}

    public function name(): string
    {
        return 'site.releases';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return ['name' => $this->site];
    }
}
