<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Writes Zimam's sshd drop-in as a guarded change (R06-D4): five minutes to
 * open a new SSH connection and keep it. Root login is only `no` or
 * `prohibit-password`.
 */
final class SshSettings extends AgentCommand
{
    /** @param list<int> $ports */
    public function __construct(
        public readonly bool $passwordAuth,
        public readonly string $rootLogin,
        public readonly array $ports,
        public readonly int $maxAuthTries,
        public readonly int $loginGraceSeconds,
    ) {}

    public function name(): string
    {
        return 'ssh.settings';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return [
            'password_auth' => $this->passwordAuth,
            'root_login' => $this->rootLogin,
            'ports' => $this->ports,
            'max_auth_tries' => $this->maxAuthTries,
            'login_grace_seconds' => $this->loginGraceSeconds,
        ];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
