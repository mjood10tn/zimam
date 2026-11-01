<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * A job that only sleeps, built into e2e agents alone, so the milestone 1
 * contract scenarios can run a job's lifecycle (D22). Its subject is
 * e2e:<subject>, and the subject follows the site-name rule.
 */
final class E2eSleep extends AgentCommand
{
    public function __construct(public readonly int $seconds, public readonly string $subject) {}

    public function name(): string
    {
        return 'e2e.sleep';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['seconds' => $this->seconds, 'subject' => $this->subject];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 180;
    }

    public function e2eOnly(): bool
    {
        return true;
    }
}
