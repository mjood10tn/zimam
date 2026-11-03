<?php

namespace App\Agent;

/** Lines read from a job's log, and the byte offset to read from next. */
final class JobLogChunk
{
    /** @param list<string> $lines without their newlines */
    public function __construct(
        public readonly array $lines,
        public readonly int $offset,
    ) {}
}
