<?php

namespace App\Agent\Exceptions;

/** No such site, job, PHP version or log archive. */
final class AgentNotFound extends AgentException
{
    protected const AGENT_CODE = 'not_found';
}
