<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Sets up a backup plan, or a plan's new settings, and its timer (R05-D7).
 * The empty lists and a panel left out are omitted, as the agent's
 * omitempty fields are.
 */
final class BackupPlan extends AgentCommand
{
    /**
     * @param  list<string>  $sites
     * @param  list<string>  $databases
     * @param  array{keep_last: int, keep_daily?: int, keep_weekly?: int, keep_monthly?: int}  $retention
     */
    public function __construct(
        public readonly string $id,
        public readonly string $destination,
        public readonly array $sites,
        public readonly array $databases,
        public readonly bool $panel,
        public readonly string $schedule,
        public readonly array $retention,
    ) {}

    public function name(): string
    {
        return 'backup.plan';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return array_filter([
            'id' => $this->id,
            'destination' => $this->destination,
            'sites' => $this->sites,
            'databases' => $this->databases,
            'panel' => $this->panel,
            'schedule' => $this->schedule,
            'retention' => array_filter($this->retention),
        ], fn (mixed $v): bool => $v !== [] && $v !== false);
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
