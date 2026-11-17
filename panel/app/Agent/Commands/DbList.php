<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Every database with its site and size, or one site's. The sizes come from
 * MySQL's statistics, so they lag a little.
 */
final class DbList extends AgentCommand
{
    public function __construct(public readonly ?string $site = null) {}

    public function name(): string
    {
        return 'db.list';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return $this->site === null ? [] : ['site' => $this->site];
    }
}
