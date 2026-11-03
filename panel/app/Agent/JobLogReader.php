<?php

namespace App\Agent;

use InvalidArgumentException;

/** Reads a job's log from a byte offset, for the live log (§7.3). */
interface JobLogReader
{
    /**
     * The whole lines written to job $jobId's log since byte $offset, and the
     * offset after them. A line still being written waits for the next read.
     *
     * @throws InvalidArgumentException for a job id that isn't a ULID
     */
    public function read(string $jobId, int $offset): JobLogChunk;
}
