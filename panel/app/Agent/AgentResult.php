<?php

namespace App\Agent;

/** A read or direct command's answer: the result line's data. */
final class AgentResult
{
    /** @param array<string, mixed> $data */
    public function __construct(public readonly array $data) {}
}
