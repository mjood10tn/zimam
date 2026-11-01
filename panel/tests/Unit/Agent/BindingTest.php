<?php

namespace Tests\Unit\Agent;

use App\Agent\AgentClient;
use App\Agent\AuditingAgentClient;
use App\Agent\FakeAgentClient;
use App\Agent\SocketAgentClient;
use App\Agent\UnixSocketTransport;
use ReflectionProperty;
use Tests\Support\EvaluatesConfigFiles;
use Tests\TestCase;

/** Which client the container gives out (design §6). */
final class BindingTest extends TestCase
{
    use EvaluatesConfigFiles;

    public function testFakeWhenEnvSet(): void
    {
        config(['zimam.agent' => 'fake']);
        $this->app->forgetInstance(AgentClient::class);

        $this->assertInstanceOf(FakeAgentClient::class, $this->app->make(AgentClient::class));
    }

    public function testSocketOtherwise(): void
    {
        config(['zimam.agent' => 'socket', 'zimam.agent_socket' => '/run/zimam/agent.sock']);
        $this->app->forgetInstance(AgentClient::class);

        $client = $this->app->make(AgentClient::class);

        $this->assertInstanceOf(AuditingAgentClient::class, $client);
        $socket = (new ReflectionProperty($client, 'inner'))->getValue($client);
        $this->assertInstanceOf(SocketAgentClient::class, $socket);
        $transport = (new ReflectionProperty($socket, 'transport'))->getValue($socket);
        $this->assertInstanceOf(UnixSocketTransport::class, $transport);
        $this->assertSame('/run/zimam/agent.sock', (new ReflectionProperty($transport, 'path'))->getValue($transport));
    }

    public function testOneClientPerApplication(): void
    {
        $this->assertSame($this->app->make(AgentClient::class), $this->app->make(AgentClient::class));
    }

    /** A server's .env that doesn't name ZIMAM_AGENT talks to the real agent. */
    public function testMissingEnvMeansTheSocket(): void
    {
        $config = $this->evaluateConfig('zimam.php', ['ZIMAM_AGENT' => null, 'ZIMAM_AGENT_SOCKET' => null]);

        $this->assertSame('socket', $config['agent']);
        $this->assertSame('/run/zimam/agent.sock', $config['agent_socket']);
    }
}
