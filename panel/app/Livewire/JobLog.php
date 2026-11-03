<?php

namespace App\Livewire;

use App\Agent\JobLogReader;
use App\Models\AgentJob;
use Illuminate\Contracts\View\View;
use Livewire\Component;

/**
 * A job's live log (§7.3): polls the log every 2 seconds from the last byte
 * offset, and stops once the job has ended and its last lines are in.
 */
class JobLog extends Component
{
    /** The newest lines kept on the page; older ones scroll away. */
    public const MAX_LINES = 2000;

    public AgentJob $agentJob;

    public int $offset = 0;

    /** @var list<string> */
    public array $lines = [];

    public bool $finished = false;

    public function mount(AgentJob $agentJob): void
    {
        $this->agentJob = $agentJob;
        $this->poll();
    }

    public function poll(): void
    {
        // Read the status first: lines written before the job ended are then
        // all in the file when the last read runs.
        $this->finished = $this->agentJob->refresh()->isFinished();
        if ($this->agentJob->agent_job_id === null) {
            return; // waiting after busy: no log yet
        }
        $chunk = app(JobLogReader::class)->read($this->agentJob->agent_job_id, $this->offset);
        $this->offset = $chunk->offset;
        if ($chunk->lines !== []) {
            $this->lines = array_slice([...$this->lines, ...$chunk->lines], -self::MAX_LINES);
        }
    }

    public function render(): View
    {
        return view('livewire.job-log');
    }
}
