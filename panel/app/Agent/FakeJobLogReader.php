<?php

namespace App\Agent;

use InvalidArgumentException;

/** The fake agent's job logs, for the live log under Herd and in tests (§14.6). */
final class FakeJobLogReader implements JobLogReader
{
    public function __construct(private readonly FakeAgentClient $agent) {}

    public function read(string $jobId, int $offset): JobLogChunk
    {
        if (preg_match(FileJobLogReader::JOB_ID, $jobId) !== 1) {
            throw new InvalidArgumentException('A job id must be a ULID');
        }
        $lines = [];
        $at = 0;
        foreach ($this->agent->logLines($jobId) as $line) {
            if ($at >= $offset) {
                $lines[] = $line;
            }
            $at += strlen($line) + 1;
        }

        return new JobLogChunk($lines, max($offset, $at));
    }
}
