<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Applies the three jails and the addresses no jail bans (R06-D5); the agent
 * installs fail2ban on first use. Answers with fail2ban's status.
 */
final class Fail2banConfig extends AgentCommand
{
    /**
     * @param  list<array{name: string, enabled: bool, max_retry: int, find_time: int, ban_time: int}>  $jails
     * @param  list<string>  $ignore
     */
    public function __construct(public readonly array $jails, public readonly array $ignore) {}

    public function name(): string
    {
        return 'fail2ban.config';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['jails' => $this->jails, 'ignore' => $this->ignore];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 600;
    }
}
