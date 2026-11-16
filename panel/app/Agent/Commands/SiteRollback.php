<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Makes a kept release live again; one already live is left as it is. */
final class SiteRollback extends AgentCommand
{
    public function __construct(
        public readonly string $site,
        public readonly string $release,
    ) {}

    public function name(): string
    {
        return 'site.rollback';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['name' => $this->site, 'release' => $this->release];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
