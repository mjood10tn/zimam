<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Log retention, the audit log's retention and the per-site cap (§11.4, D24). */
final class LogsPolicy extends AgentCommand
{
    public function __construct(
        public readonly int $retentionDays,
        public readonly int $auditRetentionDays,
        public readonly int $siteCapBytes,
    ) {}

    public function name(): string
    {
        return 'logs.policy';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['retention_days' => $this->retentionDays, 'audit_retention_days' => $this->auditRetentionDays, 'site_cap_bytes' => $this->siteCapBytes];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
