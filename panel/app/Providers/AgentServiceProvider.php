<?php

namespace App\Providers;

use App\Agent\AgentClient;
use App\Agent\AuditingAgentClient;
use App\Agent\FakeAgentClient;
use App\Agent\SocketAgentClient;
use App\Agent\UnixSocketTransport;
use Illuminate\Support\ServiceProvider;

/** Binds the agent client (design §6): the real socket, or the fake under Herd and in tests. */
class AgentServiceProvider extends ServiceProvider
{
    public function register(): void
    {
        $this->app->singleton(AgentClient::class, fn (): AgentClient => config('zimam.agent') === 'fake'
            ? FakeAgentClient::demo()
            : new AuditingAgentClient(new SocketAgentClient(new UnixSocketTransport(config('zimam.agent_socket')))));
    }
}
