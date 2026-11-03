<?php

namespace Tests\Feature\Logs;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Livewire\LogViewer;
use App\Models\Site;
use App\Models\User;
use App\Support\PanelLog;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
use Tests\TestCase;

/** The log viewer (§11.2): the last 200 lines, older on demand, live follow, search, priority, archives. */
final class LogViewerTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
    }

    /** @return list<array<string, mixed>> */
    private function reads(): array
    {
        return array_values(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === 'logs.read'), 'params'));
    }

    public function testLoadsLast200(): void
    {
        $viewer = Livewire::test(LogViewer::class, ['source' => 'file.zimam-audit']);

        $this->assertCount(200, $viewer->get('lines'));
        $this->assertSame('file.zimam-audit line 101', $viewer->get('lines')[0]['text']);
        $this->assertSame('file.zimam-audit line 300', $viewer->get('lines')[199]['text']);
        $this->assertSame([['source' => 'file.zimam-audit', 'direction' => 'back', 'limit' => 200]], $this->reads());
        $viewer->assertSee('file.zimam-audit line 300');
    }

    public function testLoadOlder(): void
    {
        $viewer = Livewire::test(LogViewer::class, ['source' => 'file.zimam-audit'])->call('loadOlder');

        $this->assertCount(300, $viewer->get('lines'));
        $this->assertSame('file.zimam-audit line 1', $viewer->get('lines')[0]['text']);
        $this->assertSame('back', $this->reads()[1]['direction']);
        $this->assertSame($this->reads()[1]['offset'], strlen(implode("\n", array_map(fn (int $i): string => "file.zimam-audit line {$i}", range(1, 100)))) + 1);
        $this->assertNull($viewer->get('back'), 'nothing older than the first line');
    }

    public function testFollowPolls3s(): void
    {
        $viewer = Livewire::test(LogViewer::class, ['source' => 'file.nginx-error'])->assertDontSeeHtml('wire:poll.3s');
        $viewer->set('follow', true)->assertSeeHtml('wire:poll.3s');

        $this->agent->setLog('file.nginx-error', array_map(fn (int $i): string => "file.nginx-error line {$i}", range(1, 302)));
        $viewer->call('poll');

        $this->assertSame('file.nginx-error line 302', $viewer->get('lines')[count($viewer->get('lines')) - 1]['text']);
        $last = $this->reads()[count($this->reads()) - 1];
        $this->assertSame('forward', $last['direction']);
        $this->assertArrayHasKey('offset', $last);
    }

    public function testSearchPassedThrough(): void
    {
        $viewer = Livewire::test(LogViewer::class, ['source' => 'file.zimam-audit'])->set('search', 'LINE 29');

        $last = $this->reads()[count($this->reads()) - 1];
        $this->assertSame('LINE 29', $last['search']);
        $this->assertSame(['file.zimam-audit line 29', 'file.zimam-audit line 290', 'file.zimam-audit line 291'], array_slice(array_column($viewer->get('lines'), 'text'), 0, 3));
        $viewer->assertSee(__('zimam.logs.search_stopped.end'))->assertDontSeeHtml('wire:poll.3s');
    }

    public function testPriorityFilterOnlyForJournal(): void
    {
        Livewire::test(LogViewer::class, ['source' => 'file.zimam-audit'])
            ->assertDontSee(__('zimam.logs.priority'))
            ->set('priority', 3);
        $this->assertArrayNotHasKey('priority', $this->reads()[count($this->reads()) - 1]);

        $journal = Livewire::test(LogViewer::class, ['source' => 'journal.nginx'])->assertSee(__('zimam.logs.priority'))->set('priority', 3);
        $this->assertSame(3, $this->reads()[count($this->reads()) - 1]['priority']);
        $this->assertNotEmpty($journal->get('lines'));
        foreach ($journal->get('lines') as $line) {
            $this->assertLessThanOrEqual(3, $line['priority']);
        }
    }

    public function testArchiveListFromAgent(): void
    {
        $viewer = Livewire::test(LogViewer::class, ['source' => 'file.zimam-audit'])
            ->assertSee('audit.log-20261101-1761955200.gz')
            ->set('archive', 'audit.log-20261101-1761955200.gz');
        $this->assertSame('audit.log-20261101-1761955200.gz', $this->reads()[count($this->reads()) - 1]['archive']);

        // A name the agent didn't list is never sent.
        $viewer->set('archive', '../../../../etc/shadow');
        $this->assertArrayNotHasKey('archive', $this->reads()[count($this->reads()) - 1]);
    }

    /** The panel reads its own Laravel log itself; nothing goes to the agent. */
    public function testPanelOwnLog(): void
    {
        $path = tempnam(sys_get_temp_dir(), 'laravel');
        file_put_contents($path, implode('', array_map(fn (int $i): string => "[2026-11-03] local.ERROR: boom {$i}\n", range(1, 250))));
        $this->app->instance(PanelLog::class, new PanelLog($path));

        $viewer = Livewire::test(LogViewer::class, ['source' => PanelLog::SOURCE]);

        $this->assertCount(200, $viewer->get('lines'));
        $this->assertSame('[2026-11-03] local.ERROR: boom 250', $viewer->get('lines')[199]['text']);
        $viewer->call('loadOlder');
        $this->assertCount(250, $viewer->get('lines'));
        $viewer->set('search', 'BOOM 12');
        $this->assertSame(['[2026-11-03] local.ERROR: boom 12', '[2026-11-03] local.ERROR: boom 120'], array_slice(array_column($viewer->get('lines'), 'text'), 0, 2));
        $this->assertSame([], $this->reads());
        unlink($path);
    }

    /** The Logs page lists the server's sources; a site's Logs tab its three. */
    public function testSourcesListed(): void
    {
        $this->get('/admin/logs')->assertOk()
            ->assertSee(__('zimam.logs.sources.journal.system'))
            ->assertSee(__('zimam.logs.sources.file.zimam-audit'))
            ->assertSee(__('zimam.logs.sources.panel.laravel'));

        $site = Site::create(['name' => 'shop', 'type' => Site::PHP, 'php_version' => '8.5', 'status' => Site::ACTIVE]);
        $site->domains()->create(['hostname' => 'shop.example.com', 'is_primary' => true]);
        $site->securitySettings()->create();
        $this->get("/admin/sites/{$site->getRouteKey()}")->assertOk()
            ->assertSee(__('zimam.logs.sources.site.nginx-access'))
            ->assertSee(__('zimam.logs.sources.site.php-error'));
    }
}
