<?php

namespace App\Livewire;

use App\Agent\AgentClient;
use App\Agent\Commands\LogsList;
use App\Agent\Commands\LogsRead;
use App\Agent\Exceptions\AgentException;
use App\Support\PanelLog;
use Livewire\Component;

/**
 * One log source (§11.2): the last 200 lines, "Load older", live follow
 * every 3 seconds, a plain-text search, a priority filter for journal
 * sources and the archives the agent lists. The panel only ever names a
 * source, a site and an archive from logs.list; the panel's own log is read
 * here, from its storage folder (§11.1).
 */
class LogViewer extends Component
{
    /** Lines kept on the page while following. */
    public const KEEP = 2000;

    public string $source;

    public ?string $site = null;

    public ?string $archive = null;

    public string $search = '';

    public ?int $priority = null;

    public bool $follow = false;

    /** @var list<array{key: string, text: string, meta: string, priority: ?int}> */
    public array $lines = [];

    /** Where "Load older" and following read from: an offset or a cursor. */
    public ?string $back = null;

    public ?string $forward = null;

    public string $stopped = '';

    public ?string $error = null;

    /** @var list<array{name: string, size: int, modified: string}> */
    public array $archives = [];

    public function mount(string $source, ?string $site = null): void
    {
        $this->source = $source;
        $this->site = $site;
        $this->archives = $this->listArchives();
        $this->load();
    }

    public function isJournal(): bool
    {
        return str_starts_with($this->source, 'journal.');
    }

    public function updatedSearch(): void
    {
        $this->load();
    }

    public function updatedPriority(): void
    {
        $this->load();
    }

    public function updatedArchive(): void
    {
        $this->follow = false;
        $this->load();
    }

    /** Reads the newest 200 lines, or the search's matches. */
    public function load(): void
    {
        $this->lines = [];
        $this->back = $this->forward = null;
        $this->apply($this->read('back'), append: true);
    }

    public function loadOlder(): void
    {
        if ($this->back !== null && $this->search === '') {
            $older = $this->read('back', $this->back);
            $kept = $this->lines;
            $this->lines = [];
            $this->apply($older, append: true, keepForward: true);
            $this->lines = [...$this->lines, ...$kept];
        }
    }

    /** Following: the lines after the last one shown. */
    public function poll(): void
    {
        if (! $this->follow || $this->archive !== null || $this->search !== '' || $this->forward === null) {
            return;
        }
        $back = $this->back;
        $this->apply($this->read('forward', $this->forward), append: true);
        $this->back = $back;
        $this->lines = array_slice($this->lines, -self::KEEP);
    }

    /** @return array<string, mixed>|null logs.read's data, or null after showing why not */
    private function read(string $direction, ?string $from = null): ?array
    {
        $this->error = null;
        $search = trim($this->search) === '' ? null : mb_substr($this->search, 0, 200);
        if ($this->source === PanelLog::SOURCE) {
            return app(PanelLog::class)->read($direction, $from === null ? null : (int) $from, 200, (string) $search);
        }
        $archive = in_array($this->archive, array_column($this->archives, 'name'), true) ? $this->archive : null;
        $journal = $this->isJournal();
        try {
            return app(AgentClient::class)->call(new LogsRead(
                source: $this->source,
                direction: $search === null ? $direction : 'back',
                site: $this->site,
                archive: $journal ? null : $archive,
                offset: ! $journal && $from !== null ? (int) $from : null,
                cursor: $journal ? $from : null,
                limit: 200,
                search: $search,
                priority: $journal ? $this->priority : null,
            ))->data;
        } catch (AgentException $e) {
            $this->error = $e->getMessage();

            return null;
        }
    }

    /** @param array<string, mixed>|null $data */
    private function apply(?array $data, bool $append, bool $keepForward = false): void
    {
        if ($data === null) {
            return;
        }
        $rows = [];
        foreach ($data['lines'] ?? [] as $l) {
            $rows[] = ['key' => 'o'.$l['offset'], 'text' => $l['text'], 'meta' => '', 'priority' => null];
        }
        foreach ($data['entries'] ?? [] as $e) {
            $rows[] = ['key' => 'c'.$e['cursor'], 'text' => $e['message'], 'meta' => trim(substr((string) $e['time'], 0, 19).' '.$e['unit']), 'priority' => $e['priority']];
        }
        $this->lines = $append ? [...$this->lines, ...$rows] : $rows;
        $back = $this->isJournal() ? ($data['back_cursor'] ?? null) : ($data['back_offset'] ?? null);
        $this->back = $back === null ? null : (string) $back;
        if (! $keepForward) {
            $forward = $this->isJournal() ? ($data['forward_cursor'] ?? null) : ($data['forward_offset'] ?? null);
            $this->forward = $forward === null ? $this->forward : (string) $forward;
        }
        $this->stopped = (string) ($data['search_stopped'] ?? '');
    }

    /** @return list<array{name: string, size: int, modified: string}> */
    private function listArchives(): array
    {
        if ($this->source === PanelLog::SOURCE || $this->isJournal()) {
            return [];
        }
        try {
            return app(AgentClient::class)->call(new LogsList($this->source, $this->site))->data['archives'] ?? [];
        } catch (AgentException) {
            return [];
        }
    }

    public function render()
    {
        return view('livewire.log-viewer', ['live' => $this->follow && $this->archive === null && $this->search === '']);
    }
}
