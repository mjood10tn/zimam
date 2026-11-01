<?php

namespace App\Agent\Exceptions;

/** The request contradicts the server's current state. */
final class AgentConflict extends AgentException
{
    protected const AGENT_CODE = 'conflict';
}
