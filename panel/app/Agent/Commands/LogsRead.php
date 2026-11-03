<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Lines of a file log, or entries of a journal one (§11.2): back from the
 * end or an offset/cursor, forward from one, or a plain-text search.
 */
final class LogsRead extends AgentCommand
{
    public function __construct(
        public readonly string $source,
        public readonly string $direction = 'back',
        public readonly ?string $site = null,
        public readonly ?string $archive = null,
        public readonly ?int $offset = null,
        public readonly ?string $cursor = null,
        public readonly ?int $limit = null,
        public readonly ?string $search = null,
        public readonly ?int $priority = null,
    ) {}

    public function name(): string
    {
        return 'logs.read';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return array_filter([
            'source' => $this->source,
            'name' => $this->site,
            'archive' => $this->archive,
            'direction' => $this->direction,
            'offset' => $this->offset,
            'cursor' => $this->cursor,
            'limit' => $this->limit,
            'search' => $this->search === '' ? null : $this->search,
            'priority' => $this->priority,
        ], fn ($v) => $v !== null);
    }

    public function isJournal(): bool
    {
        return str_starts_with($this->source, 'journal.');
    }
}
