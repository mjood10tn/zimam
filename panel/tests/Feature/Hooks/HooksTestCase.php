<?php

namespace Tests\Feature\Hooks;

use App\Models\AgentJob;
use Illuminate\Testing\TestResponse;
use Tests\Feature\Deployments\DeploymentsTestCase;

/**
 * What the hook tests share: the Deployments setup with nobody signed in,
 * since GitHub and a deploy URL's caller have no session (R02-D11).
 */
abstract class HooksTestCase extends DeploymentsTestCase
{
    protected const COMMIT = 'c0ffee0000000000000000000000000000000001';

    protected function setUp(): void
    {
        parent::setUp();
        $this->app['auth']->forgetGuards();
    }

    /** A POST of $body to $uri, as raw bytes, with $headers as HTTP headers. */
    protected function postRaw(string $uri, string $body, array $headers = [], string $ip = '127.0.0.1'): TestResponse
    {
        $server = ['REMOTE_ADDR' => $ip];
        foreach ($headers as $name => $value) {
            $key = strtoupper(str_replace('-', '_', $name));
            $server[in_array($key, ['CONTENT_TYPE', 'CONTENT_LENGTH'], true) ? $key : 'HTTP_'.$key] = $value;
        }

        return $this->call('POST', $uri, [], [], [], $server, $body);
    }

    /** A delivery as GitHub sends it, signed with $secret unless $signature is given. */
    protected function deliver(string $site, string $secret, string $event, array|string $payload, array $headers = [], ?string $signature = null, string $ip = '127.0.0.1'): TestResponse
    {
        $body = is_string($payload) ? $payload : json_encode($payload, JSON_UNESCAPED_SLASHES);

        return $this->postRaw("/hooks/github/{$site}", $body, $headers + [
            'Content-Type' => 'application/json',
            'User-Agent' => 'GitHub-Hookshot/0a1b2c3',
            'X-GitHub-Event' => $event,
            'X-GitHub-Delivery' => (string) str()->uuid(),
            'X-GitHub-Hook-ID' => '12345',
            'X-Hub-Signature-256' => $signature ?? 'sha256='.hash_hmac('sha256', $body, $secret),
        ], $ip);
    }

    /** @return array<string, mixed> a push of $commit to $branch */
    protected function push(string $branch = 'main', string $commit = self::COMMIT, array $extra = []): array
    {
        return $extra + [
            'ref' => "refs/heads/{$branch}",
            'before' => str_repeat('a', 40),
            'after' => $commit,
            'created' => false,
            'deleted' => false,
            'forced' => false,
            'commits' => [['id' => $commit, 'message' => 'Fix']],
            'head_commit' => ['id' => $commit],
            'pusher' => ['name' => 'octo'],
        ];
    }

    /** @return list<array<string, mixed>> */
    protected function deploys(): array
    {
        return $this->submitted('site.deploy');
    }

    protected function assertNoJob(): void
    {
        $this->assertSame([], $this->deploys());
        $this->assertSame(0, AgentJob::count());
    }
}
