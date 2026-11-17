<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Removes a site and everything it has on the server (§9.8). Its databases
 * go only with $dropDatabases: without it the agent refuses a site that
 * still has any.
 */
final class SiteDestroy extends AgentCommand
{
    public function __construct(public readonly string $siteName, public readonly bool $dropDatabases = false) {}

    public function name(): string
    {
        return 'site.destroy';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        $params = ['name' => $this->siteName];
        if ($this->dropDatabases) {
            $params['drop_databases'] = true;
        }

        return $params;
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
