<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;
use SensitiveParameter;

/**
 * Sets the password of a site's MySQL user, making the user if it has none.
 * The password is a secret: redacted wherever the panel records the command.
 */
final class DbUser extends AgentCommand
{
    /** What the agent accepts: it needs no escaping in SQL. */
    public const PASSWORD_PATTERN = '/^[A-Za-z0-9_-]{24,64}\z/';

    public function __construct(
        public readonly string $site,
        #[SensitiveParameter] public readonly string $password,
    ) {}

    /** 32 random bytes, base64url without padding: 43 characters. */
    public static function generatePassword(): string
    {
        return rtrim(strtr(base64_encode(random_bytes(32)), '+/', '-_'), '=');
    }

    public function name(): string
    {
        return 'db.user';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['site' => $this->site, 'password' => $this->password];
    }

    public function secretFields(): array
    {
        return ['password'];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
