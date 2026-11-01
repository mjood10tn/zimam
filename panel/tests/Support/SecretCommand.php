<?php

namespace Tests\Support;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * A job command with secrets nested in a list, as site.security's basic auth
 * users will have them (§5.3). No milestone 1 command carries a secret.
 */
final class SecretCommand extends AgentCommand
{
    public function name(): string
    {
        return 'test.secret';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return [
            'name' => 'shop',
            'token' => 'top-level secret',
            'users' => [
                ['username' => 'ann', 'password_hash' => '$2y$10$annannannannannannannannannannannannannannannannannan'],
                ['username' => 'bob', 'password_hash' => '$2y$10$bobbobbobbobbobbobbobbobbobbobbobbobbobbobbobbobbobbo'],
            ],
        ];
    }

    public function secretFields(): array
    {
        return ['token', 'users.*.password_hash'];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
