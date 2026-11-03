<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * A site's whole set of security settings (§9.6), applied together. The
 * users carry bcrypt hashes the panel made; plaintext never leaves it.
 */
final class SiteSecurity extends AgentCommand
{
    /**
     * @param  list<string>  $ipAllow
     * @param  list<string>  $ipDeny
     * @param  list<array{username: string, password_hash: string}>  $users
     */
    public function __construct(
        public readonly string $site,
        public readonly bool $forceHttps = false,
        public readonly ?int $hstsMaxAge = null,
        public readonly bool $hstsIncludeSubdomains = false,
        public readonly bool $headersPreset = true,
        public readonly bool $blockRiskyFunctions = false,
        public readonly bool $restrictToSiteFolder = false,
        public readonly array $ipAllow = [],
        public readonly array $ipDeny = [],
        public readonly bool $passwordProtection = false,
        public readonly array $users = [],
    ) {}

    public function name(): string
    {
        return 'site.security';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return [
            'name' => $this->site,
            'force_https' => $this->forceHttps,
            'hsts' => ['max_age' => $this->hstsMaxAge, 'include_subdomains' => $this->hstsIncludeSubdomains],
            'headers_preset' => $this->headersPreset,
            'block_risky_functions' => $this->blockRiskyFunctions,
            'restrict_to_site_folder' => $this->restrictToSiteFolder,
            'ip_allow' => array_values($this->ipAllow),
            'ip_deny' => array_values($this->ipDeny),
            'password_protection' => [
                'enabled' => $this->passwordProtection,
                'users' => array_map(fn (array $u): array => ['username' => $u['username'], 'password_hash' => $u['password_hash']], array_values($this->users)),
            ],
        ];
    }

    public function secretFields(): array
    {
        return ['password_protection.users.*.password_hash'];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
