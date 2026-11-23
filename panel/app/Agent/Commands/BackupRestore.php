<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Restores one part of a backup (R05-D10): a database part into a new
 * database of the site, or with $overwrite an existing one; a files part
 * into ~/restores/<backup>/ of the same site, never over the live site
 * (OQ6).
 */
final class BackupRestore extends AgentCommand
{
    public function __construct(
        public readonly string $plan,
        public readonly string $backup,
        public readonly string $part,
        public readonly string $site,
        public readonly ?string $database = null,
        public readonly bool $overwrite = false,
    ) {}

    public function name(): string
    {
        return 'backup.restore';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        $params = ['plan' => $this->plan, 'backup' => $this->backup, 'part' => $this->part, 'site' => $this->site];
        if ($this->database !== null) {
            $params['database'] = $this->database;
        }
        if ($this->overwrite) {
            $params['overwrite'] = true;
        }

        return $params;
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 21600;
    }
}
