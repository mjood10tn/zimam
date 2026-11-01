<?php

namespace App\Agent;

/**
 * Carries one request to the agent. SocketAgentClient reads and checks the
 * reply itself, so tests can hand it a memory stream instead of a socket.
 */
interface AgentTransport
{
    /**
     * Opens a connection, writes the whole request line, and returns the
     * stream the reply is read from. $readTimeout bounds each read, in
     * seconds; null waits as long as the job runs (job.follow).
     *
     * @return resource
     *
     * @throws Exceptions\AgentUnavailable when the agent can't be reached
     */
    public function send(string $line, ?int $readTimeout);
}
