<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Gets a site its certificate over HTTP-01 and turns on HTTPS (§10). */
final class CertIssue extends AgentCommand
{
    public function __construct(public readonly string $siteName) {}

    public function name(): string
    {
        return 'cert.issue';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['name' => $this->siteName];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
