<?php

namespace App\Agent\Exceptions;

/** The job's subject already has a job, or two jobs are already running (§5.7). */
final class AgentBusy extends AgentException
{
    protected const AGENT_CODE = 'busy';
}
