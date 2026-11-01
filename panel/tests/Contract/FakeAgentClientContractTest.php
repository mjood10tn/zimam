<?php

namespace Tests\Contract;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;

final class FakeAgentClientContractTest extends AgentClientContract
{
    protected function makeClient(): AgentClient
    {
        return new FakeAgentClient;
    }
}
