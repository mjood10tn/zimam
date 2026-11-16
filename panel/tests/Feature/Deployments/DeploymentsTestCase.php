<?php

namespace Tests\Feature\Deployments;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteRepository;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Tests\TestCase;

/** What the Deployments tests share: a fake agent, a signed-in owner and a site. */
abstract class DeploymentsTestCase extends TestCase
{
    use RefreshDatabase;

    protected const PASSWORD = 'the right password';

    protected FakeAgentClient $agent;

    protected User $user;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $this->user = User::factory()->create(['password' => self::PASSWORD]);
        $this->actingAs($this->user);
    }

    protected function site(string $type = Site::PHP, string $name = 'shop'): Site
    {
        $site = Site::create(['name' => $name, 'type' => $type, 'php_version' => $type === Site::STATIC ? null : '8.5', 'status' => Site::ACTIVE]);
        $site->domains()->create(['hostname' => "{$name}.example.com", 'is_primary' => true]);
        $site->securitySettings()->create();

        return $site;
    }

    /** A site whose repository is connected, in the panel and on the fake's server. */
    protected function connected(Site $site, array $settings = []): SiteRepository
    {
        $this->agent->connectRepository($site->name, 'octo/shop', 'main');
        $repository = SiteRepository::createFor($site, 'octo/shop', 'main');
        $repository->fill([
            'deploy_key_public' => 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl',
            'deploy_key_fingerprint' => 'SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU',
        ] + $settings)->save();

        return $repository;
    }

    /** @return list<array<string, mixed>> the params of every $command submitted */
    protected function submitted(string $command): array
    {
        return array_values(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit' && $c['command'] === $command), 'params'));
    }

    protected function track(AgentJob $job): AgentJob
    {
        $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
        app()->call([$track, 'handle']);

        return $job->refresh();
    }

    protected function trackToTheEnd(AgentJob $job): AgentJob
    {
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $this->track($job);
        }

        return $job;
    }
}
