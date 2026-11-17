<?php

namespace Tests\Feature\Databases;

use App\Agent\AgentClient;
use App\Agent\AuditingAgentClient;
use App\Agent\FakeAgentClient;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Queue;
use Illuminate\Support\Facades\Schema;
use Tests\TestCase;

/** What the MySQL tests share: a fake agent behind the auditing client, a signed-in owner and a site. */
abstract class DatabasesTestCase extends TestCase
{
    use RefreshDatabase;

    protected const PASSWORD = 'the right password';

    protected FakeAgentClient $agent;

    /** Tests that scan the queue table for a secret turn this off and use the database queue. */
    protected bool $fakeQueue = true;

    protected function setUp(): void
    {
        parent::setUp();
        if ($this->fakeQueue) {
            Queue::fake();
        } else {
            config(['queue.default' => 'database']);
        }
        $this->agent = new FakeAgentClient;
        // The real client's wrapper, so agent_actions is written as in production.
        $this->app->instance(AgentClient::class, new AuditingAgentClient($this->agent));
        $this->actingAs(User::factory()->create(['password' => self::PASSWORD]));
    }

    protected function site(string $name = 'shop', string $type = Site::LARAVEL): Site
    {
        $site = Site::create(['name' => $name, 'type' => $type, 'php_version' => '8.5', 'status' => Site::ACTIVE]);
        $site->domains()->create(['hostname' => "{$name}.example.com", 'is_primary' => true]);
        $site->securitySettings()->create();

        return $site;
    }

    /** A database on the fake's MySQL and in the panel, as a created one is. */
    protected function database(Site $site, string $suffix = 'main', int $size = 0): Database
    {
        $name = Database::prefixFor($site->name).$suffix;
        $this->agent->installMysql()->database($site->name, $name, $size);

        return Database::create(['site_id' => $site->id, 'name' => $name, 'size_bytes' => $size, 'status' => Database::ACTIVE]);
    }

    protected function trackToTheEnd(AgentJob $job): AgentJob
    {
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
            app()->call([$track, 'handle']);
        }

        return $job;
    }

    /** No row of any table holds any of $values: the queue's, agent_actions and agent_jobs included. */
    protected function assertNoTableHolds(string ...$values): void
    {
        $tables = array_column(Schema::getTables(), 'name');
        $this->assertContains('agent_actions', $tables);
        $this->assertContains('agent_jobs', $tables);
        $this->assertContains('jobs', $tables);
        foreach ($tables as $table) {
            foreach (DB::table($table)->get() as $row) {
                $json = json_encode($row, JSON_INVALID_UTF8_SUBSTITUTE);
                foreach ($values as $value) {
                    $this->assertStringNotContainsString($value, $json, "{$table} holds it");
                }
            }
        }
    }

    /** @return list<array<string, mixed>> the params of every $command the fake saw */
    protected function submitted(string $command): array
    {
        return array_values(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === $command), 'params'));
    }
}
