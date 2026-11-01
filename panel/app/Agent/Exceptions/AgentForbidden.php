<?php

namespace App\Agent\Exceptions;

/** The agent doesn't accept this caller. */
final class AgentForbidden extends AgentException
{
    protected const AGENT_CODE = 'forbidden';
}
