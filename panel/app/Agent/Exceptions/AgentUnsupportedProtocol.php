<?php

namespace App\Agent\Exceptions;

/** The agent doesn't speak this protocol version. */
final class AgentUnsupportedProtocol extends AgentException
{
    protected const AGENT_CODE = 'unsupported_protocol';
}
