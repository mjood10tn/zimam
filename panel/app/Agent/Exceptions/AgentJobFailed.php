<?php

namespace App\Agent\Exceptions;

/** The job ended in failure; the message holds the reason. */
final class AgentJobFailed extends AgentException
{
    protected const AGENT_CODE = 'job_failed';
}
