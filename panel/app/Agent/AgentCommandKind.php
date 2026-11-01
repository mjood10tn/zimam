<?php

namespace App\Agent;

/** How the agent runs a command (§5.3). */
enum AgentCommandKind: string
{
    /** Answers directly and changes nothing. */
    case Read = 'read';

    /** Runs as a job in its own systemd unit; submit() returns once accepted. */
    case Job = 'job';

    /** Changes something and answers directly: job.cancel. */
    case Direct = 'direct';
}
