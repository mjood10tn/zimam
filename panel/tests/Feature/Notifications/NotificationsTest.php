<?php

namespace Tests\Feature\Notifications;

use App\Actions\Notify;
use App\Agent\AgentClient;
use App\Agent\AgentJobStatus;
use App\Agent\AuditingAgentClient;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Agent\JobOutcomes;
use App\Filament\Pages\Notifications;
use App\Models\AgentJob;
use App\Models\BackupDestination;
use App\Models\BackupPlan;
use App\Models\BackupRun;
use App\Models\NotificationChannel;
use App\Models\NotificationState;
use App\Models\Site;
use App\Models\User;
use App\Notifications\PanelEvent;
use App\Settings\PanelSettings;
use App\Support\SmtpHost;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Http\Client\Request;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Notification;
use Livewire\Livewire;
use RuntimeException;
use Tests\TestCase;

/** Notifications (R05-D11, R05-D12): channels, events, debounce, escaping and the SMTP host rule. */
final class NotificationsTest extends TestCase
{
    use RefreshDatabase;

    private const SLACK = 'https://hooks.slack.com/services/T000/B000/abcdefSECRET';

    private const DISCORD = 'https://discord.com/api/webhooks/123456/tokenSECRET';

    private const TELEGRAM_TOKEN = '123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsawSECRET';

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, new AuditingAgentClient($this->agent));
        $this->actingAs(User::factory()->create(['password' => 'the right password']));
    }

    /** @param list<string> $events */
    private function channel(string $type = NotificationChannel::SLACK, array $events = NotificationChannel::EVENTS, array $settings = []): NotificationChannel
    {
        return NotificationChannel::create([
            'type' => $type,
            'name' => $type,
            'settings' => $settings + match ($type) {
                NotificationChannel::SLACK => ['webhook_url' => self::SLACK],
                NotificationChannel::DISCORD => ['webhook_url' => self::DISCORD],
                NotificationChannel::TELEGRAM => ['bot_token' => self::TELEGRAM_TOKEN, 'chat_id' => '-1001234'],
                default => ['to' => 'owner@example.com'],
            },
            'events' => $events,
            'enabled' => true,
        ]);
    }

    /** Makes the fake's disk / $percent full from now on. */
    private function diskAt(int $percent): void
    {
        $this->agent->changeMetrics(function (array $m) use ($percent): array {
            $total = $m['disks'][0]['used_bytes'] + $m['disks'][0]['available_bytes'];
            $m['disks'][0]['used_bytes'] = intdiv($total * $percent, 100);
            $m['disks'][0]['available_bytes'] = $total - $m['disks'][0]['used_bytes'];

            return $m;
        });
    }

    public function testSlackUrlMustBeHooksSlackCom(): void
    {
        foreach (['https://hooks.slack.com.evil.example/services/x', 'http://hooks.slack.com/services/x', 'https://example.com/services/x'] as $url) {
            Livewire::test(Notifications::class)
                ->callAction('add', ['name' => 'Team', 'type' => 'slack', 'webhook_url' => $url, 'events' => ['monitoring']])
                ->assertHasActionErrors(['webhook_url']);
        }
        Livewire::test(Notifications::class)
            ->callAction('add', ['name' => 'Team', 'type' => 'slack', 'webhook_url' => self::SLACK, 'events' => ['monitoring']])
            ->assertHasNoActionErrors();
        $this->assertSame(self::SLACK, NotificationChannel::sole()->settings['webhook_url']);
    }

    public function testDiscordUrlMustBeDiscordWebhook(): void
    {
        foreach (['https://discord.com/api/other/1/x', 'https://discord.com.evil.example/api/webhooks/1/x', 'https://cdn.discordapp.com/api/webhooks/1/x'] as $url) {
            Livewire::test(Notifications::class)
                ->callAction('add', ['name' => 'Ops', 'type' => 'discord', 'webhook_url' => $url, 'events' => ['monitoring']])
                ->assertHasActionErrors(['webhook_url']);
        }
        foreach ([self::DISCORD, 'https://discordapp.com/api/webhooks/9/y'] as $url) {
            Livewire::test(Notifications::class)
                ->callAction('add', ['name' => 'Ops', 'type' => 'discord', 'webhook_url' => $url, 'events' => ['monitoring']])
                ->assertHasNoActionErrors();
        }
    }

    public function testRedirectNotFollowed(): void
    {
        $channel = $this->channel();
        Http::fake([
            'hooks.slack.com/*' => Http::response('', 302, ['Location' => 'http://169.254.169.254/latest/meta-data/']),
            '*' => Http::response('metadata'),
        ]);

        try {
            $channel->notifyNow(new PanelEvent('monitoring', 'Disk full', 'detail'));
            $this->fail('A redirect counted as sent');
        } catch (RuntimeException) {
        }

        Http::assertSentCount(1);
        Http::assertNotSent(fn (Request $r): bool => str_contains($r->url(), '169.254'));
    }

    public function testWarningNotifiesOnStartAndClear(): void
    {
        Notification::fake();
        $channel = $this->channel(events: ['monitoring']);

        $this->artisan('zimam:sample-metrics')->assertSuccessful();
        Notification::assertNothingSent();

        $this->diskAt(90);
        $this->artisan('zimam:sample-metrics')->assertSuccessful();
        $this->artisan('zimam:sample-metrics')->assertSuccessful();
        Notification::assertSentToTimes($channel, PanelEvent::class, 1);
        Notification::assertSentTo($channel, PanelEvent::class, fn (PanelEvent $n): bool => $n->type === 'monitoring' && ! $n->cleared && str_contains($n->title, '/'));

        $this->diskAt(50);
        $this->artisan('zimam:sample-metrics')->assertSuccessful();
        Notification::assertSentToTimes($channel, PanelEvent::class, 2);
        Notification::assertSentTo($channel, PanelEvent::class, fn (PanelEvent $n): bool => $n->cleared);
        $this->artisan('zimam:sample-metrics')->assertSuccessful();
        Notification::assertSentToTimes($channel, PanelEvent::class, 2);
    }

    public function testWarningRepeatsAfter24Hours(): void
    {
        Notification::fake();
        $channel = $this->channel(events: ['monitoring']);
        $this->diskAt(90);

        $this->artisan('zimam:sample-metrics');
        $this->travel(23)->hours();
        $this->artisan('zimam:sample-metrics');
        Notification::assertSentToTimes($channel, PanelEvent::class, 1);
        $this->travel(2)->hours();
        $this->artisan('zimam:sample-metrics');
        Notification::assertSentToTimes($channel, PanelEvent::class, 2);
    }

    public function testUnreachableAgentNotifiesAndClears(): void
    {
        Notification::fake();
        $channel = $this->channel(events: ['agent']);
        $this->agent->respond('system.metrics', new AgentUnavailable('connection refused'), new AgentUnavailable('connection refused'));

        $this->artisan('zimam:sample-metrics');
        $this->artisan('zimam:sample-metrics');
        Notification::assertSentToTimes($channel, PanelEvent::class, 1);
        $this->agent->respond('system.metrics');
        $this->artisan('zimam:sample-metrics')->assertSuccessful();
        Notification::assertSentToTimes($channel, PanelEvent::class, 2);
    }

    public function testChannelGetsOnlyItsEvents(): void
    {
        Notification::fake();
        $backups = $this->channel(events: ['backup']);
        $warnings = $this->channel(NotificationChannel::DISCORD, events: ['monitoring']);
        $off = $this->channel(NotificationChannel::TELEGRAM);
        $off->update(['enabled' => false]);

        app(Notify::class)->event('backup:nightly:20261123T020000Z', new PanelEvent('backup', 'Backup failed', 'x'));

        Notification::assertSentTo($backups, PanelEvent::class);
        Notification::assertNotSentTo($warnings, PanelEvent::class);
        Notification::assertNotSentTo($off, PanelEvent::class);

        // Other events notify once: the same key again sends nothing.
        app(Notify::class)->event('backup:nightly:20261123T020000Z', new PanelEvent('backup', 'Backup failed', 'x'));
        Notification::assertSentToTimes($backups, PanelEvent::class, 1);
    }

    /** Finished jobs: a deploy either way, a failed certificate, failing checks, any other failure once. */
    public function testFinishedJobsNotify(): void
    {
        Notification::fake();
        $channel = $this->channel(events: ['deploy', 'job_failed', 'certificate', 'security']);
        $site = Site::create(['name' => 'shop', 'type' => 'php', 'php_version' => '8.5', 'status' => 'active']);
        $outcomes = app(JobOutcomes::class);
        $job = fn (string $command): AgentJob => AgentJob::forceCreate(['command' => $command, 'status' => 'running', 'params' => [], 'related_type' => $site->getMorphClass(), 'related_id' => $site->id]);

        $outcomes->fail($job('site.php'), 'job_failed', 'php-fpm8.5 did not start');
        $outcomes->fail($job('cert.issue'), 'job_failed', 'Let’s Encrypt said no');
        $outcomes->apply($job('site.deploy'), AgentJobStatus::fromArray(['job' => '01JBX7Q3A1B2C3D4E5F6G7H8J9', 'command' => 'site.deploy', 'subject' => 'site:shop', 'status' => 'succeeded', 'result' => ['name' => 'shop']]));
        $outcomes->apply($job('security.check'), AgentJobStatus::fromArray(['job' => '01JBX7Q3A1B2C3D4E5F6G7H8J8', 'command' => 'security.check', 'subject' => 'site:shop', 'status' => 'succeeded', 'result' => ['scope' => 'site', 'findings' => [
            ['check' => 'certificate_valid', 'status' => 'fail', 'detail' => 'expired'],
            ['check' => 'world_writable', 'status' => 'fail', 'detail' => '3 files'],
            ['check' => 'php_hidden', 'status' => 'pass', 'detail' => ''],
        ]]]));

        $types = [];
        Notification::assertSentTo($channel, PanelEvent::class, function (PanelEvent $n) use (&$types): bool {
            $types[] = $n->type;

            return true;
        });
        sort($types);
        $this->assertSame(['certificate', 'certificate', 'deploy', 'job_failed', 'security'], $types);
        Notification::assertSentTo($channel, PanelEvent::class, fn (PanelEvent $n): bool => $n->type === 'security' && str_contains($n->body, 'world_writable') && ! str_contains($n->body, 'php_hidden'));
    }

    public function testFinishedBackupRunNotifiesOnce(): void
    {
        Notification::fake();
        $channel = $this->channel(events: ['backup']);
        $destination = BackupDestination::create(['key' => 'offsite', 'type' => 's3', 'settings' => [], 'status' => 'active']);
        BackupPlan::create(['key' => 'nightly', 'backup_destination_id' => $destination->id, 'schedule' => 'hourly', 'retention' => ['keep_last' => 1], 'status' => 'active']);
        $this->agent->backupRunRecord(['plan' => 'nightly', 'backup' => '20261123T020000Z', 'status' => 'failed', 'error' => 'mysqldump exited 2']);

        $this->artisan('zimam:sync-backup-runs');
        $this->artisan('zimam:sync-backup-runs');

        Notification::assertSentToTimes($channel, PanelEvent::class, 1);
        Notification::assertSentTo($channel, PanelEvent::class, fn (PanelEvent $n): bool => str_contains($n->body, 'mysqldump exited 2'));
        $this->assertNotNull(BackupRun::sole()->notified_at);
    }

    public function testTestButtonSends(): void
    {
        $channel = $this->channel();
        Http::fake(['hooks.slack.com/*' => Http::response('ok')]);

        Livewire::test(Notifications::class)
            ->callTableAction('test', $channel)
            ->assertNotified(__('zimam.notify.test.sent'));

        Http::assertSent(fn (Request $r): bool => $r->url() === self::SLACK && str_contains($r['text'], __('zimam.notify.test.title')));
    }

    public function testAgentTextEscapedForTelegram(): void
    {
        $channel = $this->channel(NotificationChannel::TELEGRAM);
        Http::fake(['api.telegram.org/*' => Http::response(['ok' => true])]);

        $channel->notifyNow(new PanelEvent('job_failed', 'Job <failed>', 'rm <b>x</b> & "y"'));

        Http::assertSent(function (Request $r): bool {
            return $r->url() === 'https://api.telegram.org/bot'.self::TELEGRAM_TOKEN.'/sendMessage'
                && $r['parse_mode'] === 'HTML'
                && $r['chat_id'] === '-1001234'
                && str_contains($r['text'], '<b>Job &lt;failed&gt;</b>')
                && str_contains($r['text'], 'rm &lt;b&gt;x&lt;/b&gt; &amp; "y"');
        });
    }

    public function testDiscordAndSlackEscapeAndMentionNobody(): void
    {
        $discord = $this->channel(NotificationChannel::DISCORD);
        $slack = $this->channel();
        Http::fake();

        $discord->notifyNow(new PanelEvent('job_failed', 'Failed', '@everyone **bold** <@123>'));
        $slack->notifyNow(new PanelEvent('job_failed', 'Failed', '<!channel> & <https://x|y>'));

        Http::assertSent(fn (Request $r): bool => $r->url() === self::DISCORD && $r['allowed_mentions'] === ['parse' => []] && str_contains($r['content'], '\\*\\*bold\\*\\*'));
        Http::assertSent(fn (Request $r): bool => $r->url() === self::SLACK && str_contains($r['text'], '&lt;!channel&gt; &amp; &lt;https://x|y&gt;'));
    }

    public function testSecretsEncryptedAtRest(): void
    {
        $this->channel();
        $this->channel(NotificationChannel::TELEGRAM);
        app(PanelSettings::class)->setSmtp(['host' => 'smtp.example.com', 'port' => 587, 'security' => 'starttls', 'username' => 'u', 'password' => 'smtpSECRET', 'from' => 'zimam@example.com']);

        foreach (['notification_channels', 'settings'] as $table) {
            $raw = json_encode(DB::table($table)->get());
            foreach (['SECRET', 'abcdef', 'tokenSECRET'] as $secret) {
                $this->assertStringNotContainsString($secret, $raw, "{$table} holds it");
            }
        }
        $this->assertSame('smtpSECRET', app(PanelSettings::class)->smtp()['password']);
    }

    public function testSmtpPrivateHostAllowed(): void
    {
        Livewire::test(Notifications::class)
            ->callAction('smtp', ['host' => '10.0.0.5', 'port' => 25, 'security' => 'starttls', 'from' => 'zimam@example.com'])
            ->assertHasNoActionErrors();

        $this->assertSame('10.0.0.5', app(PanelSettings::class)->smtp()['host']);
    }

    public function testSmtpLinkLocalHostRefused(): void
    {
        $this->app->instance(SmtpHost::class, new class extends SmtpHost
        {
            public array $answers = ['metadata.example.com' => ['169.254.169.254']];

            public function addresses(string $host): array
            {
                return $this->answers[$host] ?? parent::addresses($host);
            }
        });
        foreach (['169.254.169.254', 'fe80::1', 'metadata.example.com'] as $host) {
            Livewire::test(Notifications::class)
                ->callAction('smtp', ['host' => $host, 'port' => 25, 'security' => 'starttls', 'from' => 'zimam@example.com'])
                ->assertHasActionErrors(['host']);
        }
        $this->assertSame([], app(PanelSettings::class)->smtp());

        // A name that resolved elsewhere when saved and to the metadata address now is refused at send.
        app(PanelSettings::class)->setSmtp(['host' => 'relay.example.com', 'port' => 25, 'security' => 'starttls', 'username' => '', 'password' => '', 'from' => 'zimam@example.com']);
        app(SmtpHost::class)->answers['relay.example.com'] = ['169.254.169.254'];
        $channel = $this->channel(NotificationChannel::EMAIL);
        $this->expectException(RuntimeException::class);
        (new PanelEvent('monitoring', 'Disk full', 'x'))->toMail($channel);
    }

    public function testFailedSendRecordsLastErrorAndRetriesDoNotBlockQueue(): void
    {
        config(['queue.default' => 'database']);
        $channel = $this->channel();
        Http::fake(['hooks.slack.com/*' => Http::response('down', 500)]);

        app(Notify::class)->event('job-failed:1', new PanelEvent('job_failed', 'Job failed', 'x'));
        dispatch(fn () => NotificationState::query()->create(['event_key' => 'marker', 'started_at' => now(), 'last_sent_at' => now()]));
        $this->assertSame(2, DB::table('jobs')->count());

        // The failing send is put back with a delay; the job behind it runs at once.
        $this->artisan('queue:work', ['--once' => true, '--stop-when-empty' => true]);
        $this->artisan('queue:work', ['--once' => true, '--stop-when-empty' => true]);
        $this->assertTrue(NotificationState::where('event_key', 'marker')->exists());
        $this->assertNull($channel->refresh()->last_error);

        for ($i = 0; $i < 2; $i++) {
            $this->travel(10)->minutes();
            $this->artisan('queue:work', ['--once' => true, '--stop-when-empty' => true]);
        }
        $this->assertSame(0, DB::table('jobs')->count());
        $this->assertStringContainsString('500', (string) $channel->refresh()->last_error);
    }
}
