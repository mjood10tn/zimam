<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Sets a site's whole list of queue workers (v0.4.0, R04-D3): the agent
 * makes the units match it, so a worker left out is removed and an empty
 * list removes them all. Static sites are refused with conflict.
 */
final class SiteWorkers extends AgentCommand
{
    /** @param list<array<string, mixed>> $workers WorkerSpecs, as SiteWorker::spec() builds them */
    public function __construct(
        public readonly string $site,
        public readonly array $workers,
    ) {}

    public function name(): string
    {
        return 'site.workers';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['name' => $this->site, 'workers' => $this->workers];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
