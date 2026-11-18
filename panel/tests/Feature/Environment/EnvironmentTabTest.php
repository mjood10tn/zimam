<?php

namespace Tests\Feature\Environment;

use App\Agent\AgentClient;
use App\Agent\AuditingAgentClient;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\FakeAgentClient;
use App\Filament\Resources\SiteResource\Pages\EnvironmentTab;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Jobs\TrackAgentJob;
use App\Models\AgentAction;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Log\Events\MessageLogged;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Event;
use Illuminate\Support\Facades\Queue;
use Illuminate\Support\Facades\Schema;
use Livewire\Features\SupportLockedProperties\CannotUpdateLockedPropertyException;
use Livewire\Features\SupportTesting\Testable;
use Livewire\Livewire;
use PHPUnit\Framework\Attributes\DataProvider;
use Tests\TestCase;

/**
 * A site's Environment tab (R02-D10): the .env is read only after the
 * password is confirmed, saved through site.env.write, and never kept by the
 * panel: not in its tables, its logs, agent_actions or the component's state.
 */
final class EnvironmentTabTest extends TestCase
{
    use RefreshDatabase;

    private const PASSWORD = 'the right password';

    private FakeAgentClient $agent;

    private string $marker;

    protected function setUp(): void
    {
        parent::setUp();
        $this->agent = new FakeAgentClient;
        // The real client's wrapper, so agent_actions is written as in production.
        $this->app->instance(AgentClient::class, new AuditingAgentClient($this->agent));
        $user = User::factory()->create(['password' => self::PASSWORD]);
        $this->actingAs($user);
        $this->marker = 'zimam-marker-'.bin2hex(random_bytes(12));
    }

    private function site(string $type = Site::LARAVEL, string $name = 'shop'): Site
    {
        $site = Site::create(['name' => $name, 'type' => $type, 'php_version' => $type === Site::STATIC ? null : '8.5', 'status' => Site::ACTIVE]);
        $site->domains()->create(['hostname' => "{$name}.example.com", 'is_primary' => true]);

        return $site;
    }

    private function revealed(Site $site): Testable
    {
        return Livewire::test(EnvironmentTab::class, ['site' => $site])
            ->callAction('reveal', ['current_password' => self::PASSWORD])
            ->assertSet('revealed', true);
    }

    /** @return list<array{method: string, command: string, params: array<string, mixed>}> */
    private function calls(string $command): array
    {
        return array_values(array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === $command));
    }

    /** $value is in none of the component's state: its snapshot or its public properties. */
    private function assertNotInState(Testable $tab, string $value): void
    {
        $this->assertStringNotContainsString($value, json_encode($tab->snapshot));
        $this->assertStringNotContainsString($value, json_encode($tab->instance()->all()));
    }

    private function trackToTheEnd(AgentJob $job): AgentJob
    {
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
            app()->call([$track, 'handle']);
        }

        return $job;
    }

    /** No row of any table holds any of $values. */
    private function assertNoTableHolds(string ...$values): void
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

    public function testEnvHiddenUntilPasswordConfirmed(): void
    {
        Queue::fake();
        $site = $this->site();
        $this->agent->env('shop', "APP_KEY={$this->marker}\n");

        $tab = Livewire::test(EnvironmentTab::class, ['site' => $site])
            ->assertDontSee($this->marker)
            ->assertSee(__('zimam.environment.reveal'))
            ->assertSet('revealed', false);
        $this->assertSame([], $this->calls('site.env.read'), 'nothing is read before the password');

        foreach (['not the password', ''] as $wrong) {
            Livewire::test(EnvironmentTab::class, ['site' => $site])
                ->callAction('reveal', ['current_password' => $wrong])
                ->assertHasActionErrors(['current_password'])
                ->assertSet('revealed', false)
                ->assertDontSee($this->marker);
        }
        $this->assertSame([], $this->calls('site.env.read'));

        $tab->callAction('reveal', ['current_password' => self::PASSWORD])
            ->assertSet('revealed', true)
            ->assertSee($this->marker);
        $this->assertSame([['method' => 'call', 'command' => 'site.env.read', 'params' => ['name' => 'shop']]], $this->calls('site.env.read'));
        // Save sends the textarea's text as save()'s argument; Cancel, inside the form, doesn't submit it.
        $tab->assertSeeHtml('x-on:submit.prevent="$wire.save($refs.content.value)"')
            ->assertSeeHtml('x-ref="content"')
            ->assertSeeHtml('wire:ignore');
        $this->assertMatchesRegularExpression('/<button[^>]*type="button"[^>]*wire:click="cancel"|<button[^>]*wire:click="cancel"[^>]*type="button"/s', $tab->html());
        $this->assertDoesNotMatchRegularExpression('/<textarea[^>]*\b(name|wire:model)/', $tab->html());
        // Shown in this response's HTML only, never carried in the component's state.
        $this->assertNotInState($tab, $this->marker);
        $tab->call('$refresh');
        $this->assertNotInState($tab, $this->marker);
        $this->assertCount(1, $this->calls('site.env.read'), 'the editor is not read again on later requests');
    }

    /** The site page has the tab, and opening the page reads no .env. */
    public function testEnvTabOnSitePageReadsNothing(): void
    {
        Queue::fake();
        $site = $this->site();
        $this->agent->env('shop', "APP_KEY={$this->marker}\n");

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->assertSee(__('zimam.sites.tabs.environment'))
            ->assertSeeLivewire(EnvironmentTab::class)
            ->assertDontSee($this->marker);
        $this->assertSame([], $this->calls('site.env.read'));
    }

    /** A leading blank line survives the textarea, which drops one newline after its tag. */
    public function testEditorKeepsLeadingNewline(): void
    {
        Queue::fake();
        $this->agent->env('shop', "\nAPP_NAME={$this->marker}\n");

        $html = $this->revealed($this->site())->html();

        $this->assertMatchesRegularExpression('/<textarea[^>]*>\n\nAPP_NAME='.$this->marker.'/', $html);
    }

    public function testEnvSaveSubmitsWrite(): void
    {
        Queue::fake();
        $site = $this->site();
        $this->agent->env('shop', "APP_KEY=old\n");
        $content = "APP_KEY={$this->marker}\nDB_PASSWORD=\"p@ss word\"\n";

        $tab = $this->revealed($site)
            ->call('save', $content)
            ->assertHasNoErrors()
            ->assertSet('revealed', false)
            ->assertNotified(__('zimam.environment.submitted', ['name' => 'shop']))
            ->assertDontSee($this->marker);

        $this->assertSame([['name' => 'shop', 'content' => $content]], array_column($this->calls('site.env.write'), 'params'));
        $job = AgentJob::sole();
        $this->assertSame('site.env.write', $job->command);
        $this->assertTrue($job->related->is($site));
        $this->assertSame(['name' => 'shop', 'content' => '[redacted]'], $job->params);
        $this->assertNotInState($tab, $this->marker);

        $this->trackToTheEnd($job);
        $this->assertSame(AgentJob::SUCCEEDED, $job->status);
        $this->assertSame($content, $this->agent->envOf('shop'));
        $this->assertNull($site->refresh()->last_error);
    }

    /** No row of any table, no log line and no session value holds the .env, before or after a save. */
    public function testEnvNeverStored(): void
    {
        // The database queue, so a queued job's payload is in a table to scan.
        config(['queue.default' => 'database']);
        $logged = [];
        Event::listen(MessageLogged::class, function (MessageLogged $e) use (&$logged): void {
            $logged[] = $e->message.' '.json_encode($e->context, JSON_INVALID_UTF8_SUBSTITUTE);
        });
        $site = $this->site();
        $old = 'old-'.$this->marker;
        $new = 'new-'.$this->marker;
        $this->agent->env('shop', "APP_KEY={$old}\n");

        $tab = $this->revealed($site)->call('save', "APP_KEY={$new}\n")->assertHasNoErrors();
        $job = AgentJob::sole();
        $this->assertSame(1, DB::table('jobs')->count(), 'the tracking job is queued in the database');
        $this->assertNoTableHolds($old, $new);

        $this->trackToTheEnd($job);
        $this->assertSame(AgentJob::SUCCEEDED, $job->status);
        $this->assertSame("APP_KEY={$new}\n", $this->agent->envOf('shop'));
        $this->assertSame(2, AgentAction::count(), 'the read and the write are both in agent_actions');
        $this->assertNoTableHolds($old, $new);
        $this->assertNotInState($tab, $old);
        $this->assertNotInState($tab, $new);
        foreach ($logged as $line) {
            $this->assertStringNotContainsString($this->marker, $line);
        }
        $this->assertStringNotContainsString($this->marker, json_encode(session()->all()));
    }

    /**
     * A busy agent refuses the save instead of queueing it (R04-D11): the
     * .env may hold passwords, and no password waits in a queue table.
     */
    public function testEnvironmentSaveRefusedWhenBusy(): void
    {
        config(['queue.default' => 'database']);
        $site = $this->site();
        $this->agent->env('shop', "APP_KEY=old\n");
        $this->agent->respond('site.env.write', new AgentBusy('site:shop already has a job running'));

        $this->revealed($site)->call('save', "APP_KEY={$this->marker}\n")
            ->assertHasNoErrors()
            ->assertNotified(__('zimam.sites.busy'))
            ->assertSet('revealed', true);

        $this->assertCount(1, $this->calls('site.env.write'), 'the agent was asked once');
        $this->assertSame(0, AgentJob::count(), 'no agent_jobs row waits');
        $this->assertSame(0, DB::table('jobs')->count(), 'no retry is queued');
        $this->assertNoTableHolds($this->marker);
    }

    public function testAgentActionRedactsEnv(): void
    {
        Queue::fake();
        $site = $this->site();
        $this->agent->env('shop', "APP_KEY={$this->marker}\n");

        $this->revealed($site)->call('save', "APP_KEY={$this->marker}-2\n");

        $rows = AgentAction::orderBy('id')->get();
        $this->assertSame(['site.env.read', 'site.env.write'], $rows->pluck('command')->all());
        // The read is audited, as the agent audits it; its params hold no secret, its result isn't kept.
        $this->assertSame(['name' => 'shop'], $rows[0]->params);
        $this->assertSame('ok', $rows[0]->outcome);
        $this->assertSame(['name' => 'shop', 'content' => '[redacted]'], $rows[1]->params);
        $this->assertSame('accepted', $rows[1]->outcome);

        // A write the agent rejects is logged redacted too (another site: shop's job hasn't ended).
        $this->agent->respond('site.env.write', new AgentValidationFailed('Invalid parameters', ['content' => 'must not contain a NUL character']));
        $this->revealed($this->site(name: 'blog'))->call('save', "APP_KEY={$this->marker}-3\n")
            ->assertHasErrors(['content'])
            ->assertSee('must not contain a NUL character');
        $rejected = AgentAction::where('outcome', 'error:validation_failed')->sole();
        $this->assertSame(['name' => 'blog', 'content' => '[redacted]'], $rejected->params);
        $this->assertStringNotContainsString($this->marker, json_encode(DB::table('agent_actions')->get()));
    }

    /** A site without a .env shows an empty editor; saving makes one. */
    public function testMissingEnvShowsEmptyEditor(): void
    {
        Queue::fake();
        $site = $this->site(Site::STATIC, 'docs');

        $this->revealed($site)
            ->assertSet('revealed', true)
            ->assertSet('missing', true)
            ->assertSee(__('zimam.environment.missing'))
            ->call('save', "GREETING=hello\n")
            ->assertHasNoErrors();

        $this->assertSame([['name' => 'docs', 'content' => "GREETING=hello\n"]], array_column($this->calls('site.env.write'), 'params'));
    }

    /** A .env the agent won't hand over (a link, too big, not UTF-8) is reported, and no editor opens. */
    public function testUnreadableEnvNotRevealed(): void
    {
        Queue::fake();
        $this->agent->respond('site.env.read', new AgentConflict('~/site/shared/.env isn’t a regular file, so it isn’t read.'));

        Livewire::test(EnvironmentTab::class, ['site' => $this->site()])
            ->callAction('reveal', ['current_password' => self::PASSWORD])
            ->assertSet('revealed', false)
            ->assertSee('~/site/shared/.env isn’t a regular file, so it isn’t read.')
            ->call('save', "APP_KEY=x\n");

        $this->assertSame([], $this->calls('site.env.write'));
    }

    /** Only a revealed editor saves: a client calling save() without the password changes nothing. */
    public function testSaveNeedsReveal(): void
    {
        Queue::fake();
        Livewire::test(EnvironmentTab::class, ['site' => $this->site()])->call('save', "APP_KEY=forged\n");

        $this->assertSame([], $this->calls('site.env.write'));
        $this->assertSame(0, AgentJob::count());
    }

    /** @return array<string, array{string, mixed}> */
    public static function lockedProperties(): array
    {
        return [
            'revealed' => ['revealed', true],
            'missing' => ['missing', true],
            'site' => ['site', null],
            'editorKey' => ['editorKey', 'forged'],
            'readError' => ['readError', 'forged'],
            'revealedAt' => ['revealedAt', 9999999999],
        ];
    }

    /** A reveal lasts as long as a password confirmation (auth.password_timeout); after that, save() asks again. */
    public function testRevealExpires(): void
    {
        Queue::fake();
        config(['auth.password_timeout' => 600]);
        $this->agent->env('shop', 'APP_KEY=x
');
        $tab = $this->revealed($this->site());

        $this->travel(601)->seconds();
        $tab->call('save', 'APP_KEY=late
')
            ->assertSet('revealed', false)
            ->assertNotified(__('zimam.environment.expired'));
        $this->assertSame([], $this->calls('site.env.write'));

        // Within the timeout, a save goes through.
        $tab = $this->revealed($this->site(name: 'blog'));
        $this->travel(599)->seconds();
        $tab->call('save', 'APP_KEY=soon
')->assertSet('revealed', false);
        $this->assertSame([['name' => 'blog', 'content' => 'APP_KEY=soon
']], array_column($this->calls('site.env.write'), 'params'));
    }

    /** Another field the agent names is reported through lang, with the field's name. */
    public function testAgentFieldErrorsThroughLang(): void
    {
        Queue::fake();
        $this->agent->env('shop', 'APP_KEY=x
')
            ->respond('site.env.write', new AgentValidationFailed('Invalid parameters', ['name' => 'is not a site']));

        $this->revealed($this->site())->call('save', 'APP_KEY=y
')
            ->assertHasErrors(['content'])
            ->assertSee(__('zimam.environment.field_error', ['field' => 'name', 'message' => 'is not a site']));
        $this->assertNotSame('zimam.environment.field_error', __('zimam.environment.field_error'));
    }

    /** The help says what a browser does to line endings. */
    public function testHelpSaysLineEndingsAreLf(): void
    {
        Queue::fake();
        $this->agent->env('shop', 'APP_KEY=x
');

        $this->revealed($this->site())->assertSee(__('zimam.environment.line_endings'));
        $this->assertStringContainsString('LF', __('zimam.environment.line_endings'));
    }

    #[DataProvider('lockedProperties')]
    public function testClientCantSetState(string $property, mixed $value): void
    {
        $this->expectException(CannotUpdateLockedPropertyException::class);

        Livewire::test(EnvironmentTab::class, ['site' => $this->site()])->set($property, $value);
    }

    public function testCancelHidesEditor(): void
    {
        Queue::fake();
        $this->agent->env('shop', "APP_KEY={$this->marker}\n");

        $tab = $this->revealed($this->site())
            ->call('cancel')
            ->assertSet('revealed', false)
            ->assertDontSee($this->marker);

        $this->assertNotInState($tab, $this->marker);
        $this->assertSame([], $this->calls('site.env.write'));
    }

    /** The tab never polls: every request would carry the page's state. */
    public function testNoPolling(): void
    {
        Queue::fake();
        $this->agent->env('shop', "APP_KEY=x\n");

        Livewire::test(EnvironmentTab::class, ['site' => $this->site()])->assertDontSeeHtml('wire:poll');
        $this->revealed($this->site(name: 'blog'))->assertDontSeeHtml('wire:poll');
    }

    /** The agent's limits, in bytes: at most 64 KiB of UTF-8 without NUL. */
    public function testContentLimits(): void
    {
        $this->assertNull(EnvironmentTab::contentError(str_repeat('a', 65536)));
        $this->assertNull(EnvironmentTab::contentError(''));
        $this->assertSame(__('zimam.environment.validation.size', ['max' => 65536]), EnvironmentTab::contentError(str_repeat('a', 65537)));
        // 30000 characters, 90000 bytes: a browser's count would pass it.
        $this->assertSame(__('zimam.environment.validation.size', ['max' => 65536]), EnvironmentTab::contentError(str_repeat('€', 30000)));
        $this->assertSame(__('zimam.environment.validation.utf8'), EnvironmentTab::contentError("A=\xC3\x28"));
        $this->assertSame(__('zimam.environment.validation.nul'), EnvironmentTab::contentError("A=\0"));
    }

    public function testSaveRefusesBadContent(): void
    {
        Queue::fake();
        $this->agent->env('shop', "APP_KEY=x\n");

        $this->revealed($this->site())
            ->call('save', str_repeat('€', 30000))
            ->assertHasErrors(['content'])
            ->assertSet('revealed', true)
            ->call('save', "A=\0")
            ->assertHasErrors(['content'])
            ->call('save', ['not' => 'text'])
            ->assertHasErrors(['content']);

        $this->assertSame([], $this->calls('site.env.write'));
    }

    /** A save waits for the site's running job to end, as every site job does. */
    public function testBusySiteNotSaved(): void
    {
        Queue::fake();
        $site = $this->site();
        $this->agent->env('shop', "APP_KEY=x\n");
        $tab = $this->revealed($site);
        $site->jobs()->create(['command' => 'site.deploy', 'status' => AgentJob::RUNNING, 'agent_job_id' => '01JBX7Q3A1B2C3D4E5F6G7H8J9', 'params' => []]);

        $tab->call('save', "APP_KEY=y\n")
            ->assertNotified(__('zimam.sites.busy'))
            ->assertSet('revealed', true);

        $this->assertSame([], $this->calls('site.env.write'));
    }

    /** A failed write is the site's last error; the next successful one clears it. */
    public function testFailedWriteSetsLastError(): void
    {
        Queue::fake();
        $site = $this->site();
        $this->agent->env('shop', "APP_KEY=x\n")
            ->scriptJob('site.env.write', ['running', 'failed'], errorCode: 'conflict', message: '~/site/shared isn’t a folder, so the .env isn’t written.');

        $this->revealed($site)->call('save', "APP_KEY=y\n");
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame('~/site/shared isn’t a folder, so the .env isn’t written.', $site->refresh()->last_error);
    }
}
