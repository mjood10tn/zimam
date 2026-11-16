<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;
use SensitiveParameter;

/**
 * Replaces a site's ~/site/shared/.env, mode 0600 (R02-D10): UTF-8 without
 * NUL, at most 64 KiB. The content is a secret: redacted wherever the panel
 * records the command.
 */
final class SiteEnvWrite extends AgentCommand
{
    /** The agent's cap, in bytes (MaxTextFileBytes). */
    public const MAX_BYTES = 65536;

    public function __construct(
        public readonly string $site,
        #[SensitiveParameter] public readonly string $content,
    ) {}

    public function name(): string
    {
        return 'site.env.write';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['name' => $this->site, 'content' => $this->content];
    }

    public function secretFields(): array
    {
        return ['content'];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
