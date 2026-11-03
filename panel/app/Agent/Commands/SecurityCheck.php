<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** The server's security checks, or one site's (§9.7). */
final class SecurityCheck extends AgentCommand
{
    /** Null for the server's checks. */
    public function __construct(public readonly ?string $site = null) {}

    public function name(): string
    {
        return 'security.check';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return $this->site === null ? ['scope' => 'server'] : ['scope' => 'site', 'name' => $this->site];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 600;
    }
}
