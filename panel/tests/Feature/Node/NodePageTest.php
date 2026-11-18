<?php

namespace Tests\Feature\Node;

use App\Filament\Pages\Node;
use App\Filament\Resources\SiteResource\Pages\DeploymentsTab;
use App\Models\AgentJob;
use Livewire\Livewire;
use Tests\Feature\Deployments\DeploymentsTestCase;

/** The Node.js page and the deploy script's Node hint (R04-D6, R04-D19). */
final class NodePageTest extends DeploymentsTestCase
{
    /** Node $major is on the fake's server, as a succeeded node.install leaves it. */
    private function installed(string $major = '24'): void
    {
        $this->agent->changeServer(function (array $status) use ($major): array {
            $status['node'] = ['installed' => true, 'major' => $major, 'version' => "{$major}.3.1"] + $status['node'];

            return $status;
        });
    }

    public function testInstallMajorSubmits(): void
    {
        Livewire::test(Node::class)
            ->assertSee(__('zimam.node.not_installed'))
            ->assertActionVisible('install')
            ->assertActionHidden('update')
            ->assertActionHidden('change')
            ->assertActionHidden('uninstall')
            ->callAction('install', ['major' => '24'])
            ->assertNotified(__('zimam.node.install.submitted', ['major' => '24']));

        $this->assertSame([['major' => '24']], $this->submitted('node.install'));
        $job = AgentJob::sole();
        $this->assertSame('node.install', $job->command);
        $this->assertSame(900, $job->runtime_max_seconds);

        // While the job runs the page offers nothing.
        Livewire::test(Node::class)->assertSee(__('zimam.node.working'))->assertActionHidden('install');

        $this->trackToTheEnd($job);

        Livewire::test(Node::class)
            ->assertSee(__('zimam.node.installed', ['version' => '24.1.0', 'major' => '24']))
            ->assertActionHidden('install')
            ->assertActionVisible('uninstall');
    }

    /** Update is the installed major submitted again (R04-D19). */
    public function testUpdateResubmitsInstalledMajor(): void
    {
        $this->installed('24');

        Livewire::test(Node::class)
            ->assertSee(__('zimam.node.installed', ['version' => '24.3.1', 'major' => '24']))
            ->callAction('update')
            ->assertNotified(__('zimam.node.update.submitted', ['major' => '24']));

        $this->assertSame([['major' => '24']], $this->submitted('node.install'));
    }

    public function testOnlyAllowlistedMajorsOffered(): void
    {
        $this->agent->changeServer(function (array $status): array {
            $status['node']['available'][] = ['major' => '22', 'end_of_life' => '2025-04-30', 'supported' => false, 'recommended' => false];

            return $status;
        });

        $page = Livewire::test(Node::class);

        $this->assertSame(['24', '26'], array_map('strval', array_keys($page->instance()->offeredMajors())));
        $this->assertStringContainsString('Apr 30, 2029', $page->instance()->offeredMajors()['26']);
    }

    /** Changing the major purges Node first, so it asks for the password and offers only the other majors. */
    public function testChangeMajorAsksConfirmation(): void
    {
        $this->installed('24');

        $page = Livewire::test(Node::class)
            ->assertActionVisible('change')
            ->mountAction('change')
            ->assertActionMounted('change');
        $this->assertSame(['26'], array_map('strval', array_keys($page->instance()->offeredMajors('24'))));

        Livewire::test(Node::class)
            ->callAction('change', ['major' => '26', 'current_password' => 'wrong'])
            ->assertHasActionErrors(['current_password']);
        $this->assertSame([], $this->submitted('node.install'));

        Livewire::test(Node::class)
            ->callAction('change', ['major' => '26', 'current_password' => self::PASSWORD])
            ->assertNotified(__('zimam.node.change.submitted', ['major' => '26']));
        $this->assertSame([['major' => '26']], $this->submitted('node.install'));
    }

    public function testUninstallAsksForThePassword(): void
    {
        $this->installed('26');

        Livewire::test(Node::class)
            ->callAction('uninstall', ['current_password' => 'wrong'])
            ->assertHasActionErrors(['current_password']);
        $this->assertSame([], $this->submitted('node.uninstall'));

        Livewire::test(Node::class)
            ->callAction('uninstall', ['current_password' => self::PASSWORD])
            ->assertNotified(__('zimam.node.uninstall.submitted'));
        $this->assertSame([[]], $this->submitted('node.uninstall'));
        $this->assertSame(600, AgentJob::sole()->runtime_max_seconds);
    }

    /** A major past its end of life can't be updated: the agent would refuse it. */
    public function testPastEolMajorOffersNoUpdate(): void
    {
        $this->agent->changeServer(function (array $status): array {
            $status['node'] = ['installed' => true, 'major' => '22', 'version' => '22.1.0', 'available' => [
                ['major' => '22', 'end_of_life' => '2025-04-30', 'supported' => false, 'recommended' => false],
                ['major' => '26', 'end_of_life' => '2029-04-30', 'supported' => true, 'recommended' => true],
            ]];

            return $status;
        });

        Livewire::test(Node::class)
            ->assertSee(__('zimam.node.ended', ['major' => '22', 'date' => 'Apr 30, 2025']))
            ->assertActionHidden('update')
            ->assertActionVisible('change');
    }

    /** A status without node is an older or blind agent: nothing is offered and nothing is claimed (docs/STATUS.md). */
    public function testStatusWithoutNodeOffersNothing(): void
    {
        $this->agent->changeServer(function (array $status): array {
            unset($status['node']);

            return $status;
        });

        Livewire::test(Node::class)
            ->assertSee(__('zimam.node.unknown'))
            ->assertDontSee(__('zimam.node.not_installed'))
            ->assertActionHidden('install')
            ->assertActionHidden('uninstall');
    }

    public function testDeployHintShowsNodeVersion(): void
    {
        $site = $this->site();
        $this->connected($site);

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertSee(__('zimam.deployments.settings.script_node_missing'))
            ->assertSee(Node::getUrl());

        // The status is cached for 10 seconds.
        $this->travel(11)->seconds();
        $this->installed('26');

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertSee(__('zimam.deployments.settings.script_node_installed', ['version' => '26.3.1']))
            ->assertDontSee(__('zimam.deployments.settings.script_node_missing'));
    }
}
