<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Replaces the owner's firewall rules as a guarded change (R06-D1, D3): the
 * agent keeps the locked rules itself and answers with the change to keep
 * before its deadline.
 */
final class FirewallRules extends AgentCommand
{
    /** @param list<array<string, mixed>> $rules FirewallRule specs, as FirewallRule::spec() builds them */
    public function __construct(public readonly array $rules) {}

    public function name(): string
    {
        return 'firewall.rules';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['rules' => $this->rules];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
