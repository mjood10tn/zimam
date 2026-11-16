<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * A site's ~/site/shared/.env, at most 64 KiB (R02-D10). Its result is a
 * secret, so the agent audits the read, and so does the panel's
 * agent_actions; the content itself is never kept. A site without one
 * answers not_found.
 */
final class SiteEnvRead extends AgentCommand
{
    public function __construct(public readonly string $site) {}

    public function name(): string
    {
        return 'site.env.read';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return ['name' => $this->site];
    }

    public function audited(): bool
    {
        return true;
    }
}
