<?php

namespace App\Agent\Exceptions;

/** The agent failed unexpectedly; its journal has the details. */
final class AgentInternal extends AgentException
{
    protected const AGENT_CODE = 'internal';
}
