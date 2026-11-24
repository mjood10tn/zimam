<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Turns Cloudflare mode on or off (R06-D7) and, in it, opens 80 and 443 only
 * to Cloudflare (R06-D9). Only a change to the origin restriction is
 * guarded: then the result names the change to keep.
 */
final class CloudflareMode extends AgentCommand
{
    public function __construct(public readonly bool $enabled, public readonly bool $restrictOrigin) {}

    public function name(): string
    {
        return 'cloudflare.mode';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['enabled' => $this->enabled, 'restrict_origin' => $this->restrictOrigin];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
