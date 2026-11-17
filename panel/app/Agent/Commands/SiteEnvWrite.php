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

    /** The agent's checkTextFile, in bytes as the agent counts them: null when $content can be saved. */
    public static function contentError(#[SensitiveParameter] string $content): ?string
    {
        return match (true) {
            strlen($content) > self::MAX_BYTES => __('zimam.environment.validation.size', ['max' => self::MAX_BYTES]),
            ! mb_check_encoding($content, 'UTF-8') => __('zimam.environment.validation.utf8'),
            str_contains($content, "\0") => __('zimam.environment.validation.nul'),
            default => null,
        };
    }

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
