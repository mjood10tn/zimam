<?php

namespace App\Agent\Exceptions;

/** The request wasn't valid JSON, was too long, or missed an envelope field. */
final class AgentInvalidRequest extends AgentException
{
    protected const AGENT_CODE = 'invalid_request';
}
