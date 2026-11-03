<?php

namespace Tests\Feature\PhpVersions;

use App\Actions\SyncPhpVersions;
use App\Agent\AgentClient;
use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Agent\ServerStatus;
use App\Models\AgentJob;
use App\Models\PhpVersion;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

/** php_versions follows what system.status reports (§7.5, §7.6). */
final class SyncPhpVersionsTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs(User::factory()->create());
    }

    /**
     * @param  list<array{string, bool}>  $installed  version and in_use
     * @param  array<string, string>  $ends  security_end per version, where it differs from the fake's
     */
    private function reports(array $installed, string $default, string $panel, array $ends = []): void
    {
        $status = $this->agent->call(new SystemStatus)->data;
        $status['php']['installed'] = array_map(fn (array $v): array => ['version' => $v[0], 'fpm_active' => $v[1], 'in_use' => $v[1]], $installed);
        $status['php']['default'] = $default;
        $status['php']['panel'] = $panel;
        foreach ($status['php']['available'] as $i => $available) {
            $status['php']['available'][$i]['security_end'] = $ends[$available['version']] ?? $available['security_end'];
        }
        $this->agent->respond('system.status', $status);
    }

    private function sync(): bool
    {
        return app(SyncPhpVersions::class)->run();
    }

    /** @return array<string, array<string, mixed>> */
    private function rows(): array
    {
        return PhpVersion::orderBy('version')->get()
            ->mapWithKeys(fn (PhpVersion $v): array => [$v->version => [
                'status' => $v->status, 'is_default' => $v->is_default, 'is_panel' => $v->is_panel, 'in_use' => $v->in_use,
            ]])
            ->all();
    }

    private function jobFor(PhpVersion $version, string $command, string $status): AgentJob
    {
        $job = new AgentJob(['command' => $command, 'status' => $status, 'params' => ['version' => $version->version]]);
        $job->related()->associate($version);
        $job->save();

        return $job;
    }

    public function testSyncFromStatus(): void
    {
        $this->reports([['8.3', false], ['8.4', true], ['8.5', true]], default: '8.4', panel: '8.5');
        // Gone from the server: removed over SSH, say.
        PhpVersion::create(['version' => '8.2', 'status' => PhpVersion::INSTALLED]);
        // Still marked installing, though its job ended without an outcome.
        $stale = PhpVersion::create(['version' => '8.1', 'status' => PhpVersion::INSTALLING]);
        $this->jobFor($stale, 'php.install', AgentJob::FAILED);

        $this->assertTrue($this->sync());

        $this->assertSame([
            '8.1' => ['status' => PhpVersion::FAILED, 'is_default' => false, 'is_panel' => false, 'in_use' => false],
            '8.3' => ['status' => PhpVersion::INSTALLED, 'is_default' => false, 'is_panel' => false, 'in_use' => false],
            '8.4' => ['status' => PhpVersion::INSTALLED, 'is_default' => true, 'is_panel' => false, 'in_use' => true],
            '8.5' => ['status' => PhpVersion::INSTALLED, 'is_default' => false, 'is_panel' => true, 'in_use' => true],
        ], $this->rows());
    }

    /** A row whose job hasn't finished keeps its status: the job's outcome sets it. */
    public function testSyncLeavesRowsWithUnfinishedJobs(): void
    {
        $this->reports([['8.3', false], ['8.5', true]], default: '8.5', panel: '8.5');
        $installing = PhpVersion::create(['version' => '8.4', 'status' => PhpVersion::INSTALLING]);
        $this->jobFor($installing, 'php.install', AgentJob::WAITING);
        $uninstalling = PhpVersion::create(['version' => '8.3', 'status' => PhpVersion::UNINSTALLING]);
        $this->jobFor($uninstalling, 'php.uninstall', AgentJob::RUNNING);

        $this->sync();

        $this->assertSame(PhpVersion::INSTALLING, $installing->refresh()->status);
        $this->assertSame(PhpVersion::UNINSTALLING, $uninstalling->refresh()->status);
    }

    /** End dates come from the agent, never from the panel's code (D19). */
    public function testEolFromAgent(): void
    {
        $this->reports([['8.4', true]], default: '8.4', panel: '8.4', ends: ['8.4' => '2031-06-30']);

        $this->sync();

        $this->assertSame('2031-06-30', PhpVersion::where('version', '8.4')->sole()->eol_date->toDateString());
    }

    /** After an outcome the 10-second cache would hold the answer from before the job. */
    public function testSyncAsksTheAgentNotTheCache(): void
    {
        $this->reports([['8.5', true]], default: '8.5', panel: '8.5');
        ServerStatus::current();
        $this->reports([['8.4', false], ['8.5', true]], default: '8.5', panel: '8.5');

        $this->sync();

        $this->assertSame(['8.4', '8.5'], PhpVersion::orderBy('version')->pluck('version')->all());
        $this->assertCount(2, ServerStatus::current()->data['php']['installed']);
    }

    public function testSyncWithoutTheAgentChangesNothing(): void
    {
        PhpVersion::create(['version' => '8.4', 'status' => PhpVersion::INSTALLED]);
        $this->agent->respond('system.status', new AgentUnavailable('connect: no such file'));

        $this->assertFalse($this->sync());

        $this->assertSame(['8.4'], PhpVersion::pluck('version')->all());
    }
}
