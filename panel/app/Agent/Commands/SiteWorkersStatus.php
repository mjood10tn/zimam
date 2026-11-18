<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Each worker of a site with its processes as systemd reports them:
 * ActiveState, SubState, NRestarts, since when, and whether it is in a
 * crash loop (failing). A site without workers has an empty list.
 */
final class SiteWorkersStatus extends AgentCommand
{
    public function __construct(public readonly string $site) {}

    public function name(): string
    {
        return 'site.workers.status';
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
