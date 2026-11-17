<?php

namespace Tests\Feature\Cache;

use App\Filament\Pages\Cache;
use App\Models\AgentJob;
use App\Models\ServerService;
use Livewire\Livewire;
use Tests\Feature\Databases\DatabasesTestCase;

/** The Cache page: install Valkey and see how the install went. */
final class CachePageTest extends DatabasesTestCase
{
    public function testInstallValkeySubmits(): void
    {
        Livewire::test(Cache::class)
            ->assertSee(__('zimam.cache.not_installed'))
            ->assertActionVisible('install')
            ->callAction('install')
            ->assertNotified(__('zimam.cache.install.submitted'));

        $this->assertSame([[]], $this->submitted('valkey.install'));
        $this->assertSame(ServerService::INSTALLING, ServerService::named(ServerService::VALKEY)->status);
        $job = AgentJob::sole();
        $this->assertSame('valkey.install', $job->command);

        $this->trackToTheEnd($job);

        $row = ServerService::named(ServerService::VALKEY);
        $this->assertSame(ServerService::INSTALLED, $row->status);
        $this->assertSame('9.0.4', $row->version);
        Livewire::test(Cache::class)
            ->assertActionHidden('install')
            ->assertSee(__('zimam.cache.installed', ['version' => '9.0.4']));
        // MySQL's row is none of its business.
        $this->assertNull(ServerService::named(ServerService::MYSQL));
    }

    /** The agent refuses to take over a data directory from before Zimam: its words are shown, and install can be tried again. */
    public function testFailedInstallShowsTheAgentsReasonAndAllowsAnotherTry(): void
    {
        $reason = '/var/lib/valkey already holds data from before Zimam managed Valkey. Move it away and install again.';
        $this->agent->scriptJob('valkey.install', ['running', 'failed'], errorCode: 'conflict', message: $reason);

        Livewire::test(Cache::class)->callAction('install');
        $this->trackToTheEnd(AgentJob::sole());

        $row = ServerService::named(ServerService::VALKEY);
        $this->assertSame(ServerService::FAILED, $row->status);
        $this->assertSame($reason, $row->last_error);
        Livewire::test(Cache::class)->assertSee($reason)->assertActionVisible('install');
    }

    public function testCancelledInstallSaysSo(): void
    {
        $this->agent->scriptJob('valkey.install', ['running', 'cancelled']);

        Livewire::test(Cache::class)->callAction('install');
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(__('zimam.cache.install.cancelled'), ServerService::named(ServerService::VALKEY)->last_error);
    }

    public function testInstallIsOfferedOnlyWhileValkeyIsMissing(): void
    {
        $this->agent->installValkey();

        Livewire::test(Cache::class)->assertActionHidden('install')->assertDontSee(__('zimam.cache.not_installed'));
    }

    /** A page opened after the agent says Valkey is there records it. */
    public function testPageLoadSyncsTheServiceRow(): void
    {
        $this->agent->installValkey('9.0.4');

        Livewire::test(Cache::class);

        $this->assertSame(ServerService::INSTALLED, ServerService::named(ServerService::VALKEY)->status);
    }
}
