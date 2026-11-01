<?php

namespace App\Agent;

/** A job the agent accepted (§5.7). */
final class AgentJob
{
    public function __construct(public readonly string $id) {}
}
