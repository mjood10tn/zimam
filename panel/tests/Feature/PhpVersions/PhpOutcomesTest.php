<?php

namespace Tests\Feature\PhpVersions;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Filament\Pages\PhpVersions;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\PhpVersion;
use App\Models\User;
use Filament\Actions\Testing\TestAction;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** What each php.* job's outcome changes in php_versions (§7.4). */
final class PhpOutcomesTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs(User::factory()->create(['password' => 'the right password']));
    }

    /** Runs TrackAgentJob until the job ends, as the queue would. */
    private function trackToTheEnd(string $command): AgentJob
    {
        $job = AgentJob::where('command', $command)->latest('id')->firstOrFail();
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
            app()->call([$track, 'handle']);
        }

        return $job;
    }

    private function row(string $version): ?PhpVersion
    {
        return PhpVersion::where('version', $version)->first();
    }

    public function testOutcomeUpdatesStatus(): void
    {
        $page = Livewire::test(PhpVersions::class)->callAction('install', ['version' => '8.4']);
        $this->assertSame(PhpVersion::INSTALLING, $this->row('8.4')->status);

        $this->assertSame(AgentJob::SUCCEEDED, $this->trackToTheEnd('php.install')->status);
        $this->assertSame(PhpVersion::INSTALLED, $this->row('8.4')->status);
        $this->assertFalse($this->row('8.4')->is_default);

        $page->callAction(TestAction::make('setDefault')->table($this->row('8.4')));
        $this->trackToTheEnd('php.default');
        $this->assertTrue($this->row('8.4')->is_default);
        $this->assertFalse($this->row('8.5')->is_default);

        $page->callAction(TestAction::make('setDefault')->table($this->row('8.5')));
        $this->trackToTheEnd('php.default');
        $page->callAction(TestAction::make('uninstall')->table($this->row('8.4')), ['current_password' => 'the right password']);
        $this->assertSame(PhpVersion::UNINSTALLING, $this->row('8.4')->status);
        $this->trackToTheEnd('php.uninstall');
        $this->assertNull($this->row('8.4'));
    }

    public function testFailedInstallMarksFailed(): void
    {
        $this->agent->scriptJob('php.install', ['running', 'failed'], errorCode: 'internal', message: 'apt-get exited with code 100');

        Livewire::test(PhpVersions::class)->callAction('install', ['version' => '8.4']);
        $job = $this->trackToTheEnd('php.install');

        $this->assertSame(AgentJob::FAILED, $job->status);
        $this->assertSame(PhpVersion::FAILED, $this->row('8.4')->status);
        // A failed install can be tried again.
        $this->assertArrayHasKey('8.4', Livewire::test(PhpVersions::class)->instance()->installableVersions());
    }

    /** A failed uninstall leaves the version installed. */
    public function testFailedUninstallKeepsInstalled(): void
    {
        $page = Livewire::test(PhpVersions::class)->callAction('install', ['version' => '8.4']);
        $this->trackToTheEnd('php.install');
        $this->agent->scriptJob('php.uninstall', ['running', 'failed'], errorCode: 'conflict', message: 'Sites still run on PHP 8.4.');

        $page->callAction(TestAction::make('uninstall')->table($this->row('8.4')), ['current_password' => 'the right password']);
        $this->trackToTheEnd('php.uninstall');

        $this->assertSame(PhpVersion::INSTALLED, $this->row('8.4')->status);
    }
}
