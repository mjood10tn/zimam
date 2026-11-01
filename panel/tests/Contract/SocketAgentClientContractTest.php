<?php

namespace Tests\Contract;

use App\Agent\AgentClient;
use App\Agent\SocketAgentClient;
use App\Agent\UnixSocketTransport;

/**
 * The scenarios against a real agent. They run on the end-to-end machine,
 * as zimam, with ZIMAM_AGENT_SOCKET naming the socket (§6, §14.2).
 */
final class SocketAgentClientContractTest extends AgentClientContract
{
    protected function setUp(): void
    {
        if (! getenv('ZIMAM_AGENT_SOCKET')) {
            $this->markTestSkipped('ZIMAM_AGENT_SOCKET is not set; these run against a real agent');
        }
        parent::setUp();
    }

    protected function makeClient(): AgentClient
    {
        return new SocketAgentClient(new UnixSocketTransport(getenv('ZIMAM_AGENT_SOCKET')));
    }
}
