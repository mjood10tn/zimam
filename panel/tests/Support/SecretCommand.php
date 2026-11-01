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
    /**
     * Held in properties, as real commands hold their params, so a queued
     * copy of the command carries the secrets.
     *
     * @param  list<array{username: string, password_hash: string}>  $users
     */
    public function __construct(
        public readonly string $token = 'top-level secret',
        public readonly array $users = [
            ['username' => 'ann', 'password_hash' => '$2y$10$annannannannannannannannannannannannannannannannannan'],
            ['username' => 'bob', 'password_hash' => '$2y$10$bobbobbobbobbobbobbobbobbobbobbobbobbobbobbobbobbobbo'],
        ],
    ) {}

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
            'token' => $this->token,
            'users' => $this->users,
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
