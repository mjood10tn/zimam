<?php

namespace App\Agent\Exceptions;

/** The agent has no such command. */
final class AgentUnknownCommand extends AgentException
{
    protected const AGENT_CODE = 'unknown_command';
}
