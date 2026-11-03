<?php

namespace App\Providers;

use App\Agent\AgentClient;
use App\Agent\AgentHealth;
use App\Agent\AuditingAgentClient;
use App\Agent\FakeAgentClient;
use App\Agent\FakeJobLogReader;
use App\Agent\FileJobLogReader;
use App\Agent\JobLogReader;
use App\Agent\JobOutcomes;
use App\Agent\Outcomes\CertificateOutcomes;
use App\Agent\Outcomes\PhpOutcomes;
use App\Agent\Outcomes\SecurityOutcomes;
use App\Agent\Outcomes\SettingsOutcomes;
use App\Agent\Outcomes\SiteOutcomes;
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
        // Tasks 25-36 register their outcome handlers on the one instance.
        $this->app->singleton(JobOutcomes::class, function (): JobOutcomes {
            $outcomes = new JobOutcomes;
            PhpOutcomes::register($outcomes);
            SiteOutcomes::register($outcomes);
            SecurityOutcomes::register($outcomes);
            SettingsOutcomes::register($outcomes);
            CertificateOutcomes::register($outcomes);

            return $outcomes;
        });
        // The live job log reads the agent's log files, or the fake's lines.
        $this->app->bind(JobLogReader::class, fn (): JobLogReader => $this->app->make(AgentClient::class) instanceof FakeAgentClient
            ? new FakeJobLogReader($this->app->make(AgentClient::class))
            : new FileJobLogReader(config('zimam.job_logs')));
        // One answer per request or queued job, so a worker never keeps a stale one.
        $this->app->scoped(AgentHealth::class);
    }
}
