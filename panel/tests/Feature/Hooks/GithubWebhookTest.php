<?php

namespace Tests\Feature\Hooks;

use App\Agent\AgentClient;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Models\Deployment;
use App\Models\SiteRepository;
use App\Models\WebhookDelivery;
use Filament\Notifications\NotificationMailbox;

/** POST /hooks/github/{site}: GitHub's push deliveries deploy the branch (R02-D11, A10). */
final class GithubWebhookTest extends HooksTestCase
{
    public function testValidPushDeploysTheCommit(): void
    {
        $site = $this->site();
        $repository = $this->connected($site, ['auto_deploy' => true]);

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['X-GitHub-Delivery' => 'd-1'])
            ->assertStatus(202);

        $deploys = $this->deploys();
        $this->assertCount(1, $deploys);
        $this->assertSame('shop', $deploys[0]['name']);
        $this->assertSame('webhook', $deploys[0]['trigger']);
        $this->assertSame(self::COMMIT, $deploys[0]['commit']);
        $deployment = Deployment::sole();
        $this->assertSame($site->id, $deployment->site_id);
        $this->assertSame('webhook', $deployment->trigger);
        $this->assertSame(self::COMMIT, $deployment->commit);
        $this->assertNull($deployment->created_by);
        $this->assertNull($deployment->agentJob->created_by);
        $this->assertTrue(WebhookDelivery::where('delivery_id', 'd-1')->where('site_id', $site->id)->exists());
    }

    public function testPushToOtherBranchIgnored(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push('develop'))->assertStatus(202);
        // A tag named like the branch isn't the branch.
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(extra: ['ref' => 'refs/tags/main']))->assertStatus(202);

        $this->assertNoJob();
    }

    public function testDeletedBranchPushIgnored(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(commit: str_repeat('0', 40), extra: ['deleted' => true, 'head_commit' => null]))
            ->assertStatus(202);

        $this->assertNoJob();
    }

    public function testAutoDeployOffIgnoresPush(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => false]);

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push())->assertStatus(202);

        $this->assertNoJob();
    }

    public function testBadSignatureRejectedAndNoJob(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $body = json_encode($this->push());

        $wrongKey = $this->deliver('shop', 'not the secret', 'push', $body);
        $tampered = $this->deliver('shop', $repository->webhook_secret, 'push', $body, signature: 'sha256='.hash_hmac('sha256', $body.' ', $repository->webhook_secret));
        $sha1 = $this->deliver('shop', $repository->webhook_secret, 'push', $body, signature: 'sha1='.hash_hmac('sha1', $body, $repository->webhook_secret));
        $upper = $this->deliver('shop', $repository->webhook_secret, 'push', $body, signature: 'SHA256='.hash_hmac('sha256', $body, $repository->webhook_secret));

        foreach ([$wrongKey, $tampered, $sha1, $upper] as $response) {
            $response->assertNotFound();
            $this->assertSame($this->unknownSiteBody(), $response->getContent());
        }
        $this->assertNoJob();
        $this->assertSame(0, WebhookDelivery::count());
    }

    public function testMissingSignatureRejected(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $body = json_encode($this->push());

        $response = $this->postRaw('/hooks/github/shop', $body, [
            'Content-Type' => 'application/json',
            'X-GitHub-Event' => 'push',
            'X-GitHub-Delivery' => 'd-1',
            // The legacy SHA-1 header alone isn't enough.
            'X-Hub-Signature' => 'sha1='.hash_hmac('sha1', $body, $repository->webhook_secret),
        ]);

        $response->assertNotFound();
        $this->assertSame($this->unknownSiteBody(), $response->getContent());
        $this->assertNoJob();
    }

    public function testUnknownSiteIs404(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $this->site(name: 'blog');

        $unknown = $this->deliver('nosuchsite', $repository->webhook_secret, 'push', $this->push());
        // A site without a repository looks the same as no site.
        $noRepository = $this->deliver('blog', $repository->webhook_secret, 'push', $this->push());

        $unknown->assertNotFound();
        $noRepository->assertNotFound();
        $this->assertSame($unknown->getContent(), $noRepository->getContent());
        $this->assertSame($this->unknownSiteBody(), $unknown->getContent());
        $this->assertNoJob();
    }

    public function testPingAnswered(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);

        $this->deliver('shop', $repository->webhook_secret, 'ping', ['zen' => 'Keep it logically awesome.', 'hook_id' => 12345, 'hook' => ['type' => 'Repository']])
            ->assertOk();

        $this->assertNoJob();
    }

    public function testPingWithBadSignatureIs404(): void
    {
        $this->connected($this->site(), ['auto_deploy' => true]);

        $this->deliver('shop', 'not the secret', 'ping', ['zen' => 'x'])->assertNotFound();
    }

    public function testOtherEventsIgnored(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);

        $this->deliver('shop', $repository->webhook_secret, 'issues', ['action' => 'opened'])->assertStatus(202);

        $this->assertNoJob();
    }

    public function testDuplicateDeliveryIgnored(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['X-GitHub-Delivery' => 'd-1'])->assertStatus(202);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['X-GitHub-Delivery' => 'd-1'])->assertStatus(202);

        $this->assertCount(1, $this->deploys());
        $this->assertSame(1, Deployment::count());
    }

    /** R02-D12: two different pushes both deploy; the later one waits for the first and goes last. */
    public function testTwoPushesDeployInOrder(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $second = 'c0ffee0000000000000000000000000000000002';

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push())->assertStatus(202);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(commit: $second))->assertStatus(202);

        $this->assertSame([self::COMMIT, $second], Deployment::orderBy('id')->pluck('commit')->all());
        // The second found the site busy, so it waits for the D8 retry.
        $this->assertSame([Deployment::QUEUED, Deployment::WAITING], Deployment::orderBy('id')->pluck('status')->all());
    }

    public function testOversizedBodyRejected(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $limit = 25 * 1024 * 1024;
        $body = $this->paddedPush($limit + 1);
        $this->assertSame($limit + 1, strlen($body));

        // Signed correctly, and still refused for its size.
        $this->deliver('shop', $repository->webhook_secret, 'push', $body)->assertStatus(413);
        // A Content-Length header over the limit is refused before the body is read.
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['Content-Length' => (string) ($limit + 1)])->assertStatus(413);

        $this->assertNoJob();
    }

    public function testBodyAtTheLimitAccepted(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $limit = 25 * 1024 * 1024;
        $body = $this->paddedPush($limit);

        $this->deliver('shop', $repository->webhook_secret, 'push', $body)->assertStatus(202);

        $this->assertCount(1, $this->deploys());
    }

    /** A10: only application/json; a caller that can't sign learns nothing from the content type. */
    public function testFormEncodedDeliveryRefused(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $body = 'payload='.urlencode(json_encode($this->push()));

        $this->deliver('shop', $repository->webhook_secret, 'push', $body, ['Content-Type' => 'application/x-www-form-urlencoded'])
            ->assertStatus(415);
        $this->deliver('shop', 'not the secret', 'push', $body, ['Content-Type' => 'application/x-www-form-urlencoded'])
            ->assertNotFound();
        $this->deliver('nosuchsite', 'not the secret', 'push', $body, ['Content-Type' => 'application/x-www-form-urlencoded'])
            ->assertNotFound();
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['Content-Type' => 'application/json; charset=utf-8'])
            ->assertStatus(202);

        $this->assertCount(1, $this->deploys());
    }

    public function testMalformedPushRefused(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);

        $this->deliver('shop', $repository->webhook_secret, 'push', '{not json')->assertStatus(400);
        $this->deliver('shop', $repository->webhook_secret, 'push', '"main"')->assertStatus(400);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(commit: 'C0FFEE0000000000000000000000000000000001'))->assertStatus(400);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(commit: 'main'))->assertStatus(400);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(extra: ['ref' => ['refs/heads/main']]))->assertStatus(400);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['X-GitHub-Delivery' => ''])->assertStatus(400);

        $this->assertNoJob();
    }

    public function testRepositoryNotConnectedYet(): void
    {
        $site = $this->site();
        $repository = SiteRepository::createFor($site, 'octo/shop', 'main');
        $repository->update(['auto_deploy' => true]);

        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push())->assertStatus(409);

        $this->assertNoJob();
    }

    /** SubmitAgentCommand halts when the agent is down; the hook answers 503 by itself, and GitHub may redeliver. */
    public function testAgentUnavailableAnswers503AndRedeliveryDeploys(): void
    {
        $repository = $this->connected($this->site(), ['auto_deploy' => true]);
        $this->agent->respond('site.deploy', new AgentUnavailable('down'));
        // There's no one to notify: nothing goes into Filament's mailbox, which would cache it under a throwaway session.
        $mailbox = $this->spy(NotificationMailbox::class);

        $response = $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['X-GitHub-Delivery' => 'd-1']);

        $response->assertStatus(503);
        $this->assertSame([], $response->headers->getCookies());
        $this->assertSame(0, Deployment::count());
        $this->assertSame(0, WebhookDelivery::count());
        $mailbox->shouldNotHaveReceived('push');

        // The agent is back: GitHub's redelivery keeps the delivery id.
        $this->agent = (new FakeAgentClient)->connectRepository('shop', 'octo/shop', 'main');
        $this->app->instance(AgentClient::class, $this->agent);
        $this->deliver('shop', $repository->webhook_secret, 'push', $this->push(), ['X-GitHub-Delivery' => 'd-1'])->assertStatus(202);
        $this->assertCount(1, $this->deploys());
    }

    /**
     * A push padded with JSON whitespace to $bytes. A few copies of it are
     * alive at once, more than PHP's default 128M allows.
     */
    private function paddedPush(int $bytes): string
    {
        ini_set('memory_limit', '512M');
        $json = json_encode($this->push());

        return str_pad(substr($json, 0, -1), $bytes - 1).'}';
    }

    private function unknownSiteBody(): string
    {
        return (string) $this->postRaw('/hooks/github/nosuchsite', '{}', ['Content-Type' => 'application/json'])->getContent();
    }
}
