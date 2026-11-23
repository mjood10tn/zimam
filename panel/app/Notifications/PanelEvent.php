<?php

namespace App\Notifications;

use App\Models\NotificationChannel;
use App\Notifications\Channels\WebhookChannel;
use App\Settings\PanelSettings;
use App\Support\SmtpHost;
use Illuminate\Bus\Queueable;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Notifications\Messages\MailMessage;
use Illuminate\Notifications\Notification;
use RuntimeException;
use Throwable;

/**
 * One event the panel tells the owner about (R05-D11, R05-D12): a monitoring
 * warning, a failed job, a backup's outcome, and so on. $type is the event
 * a channel subscribes to (NotificationChannel::EVENTS). Sent on the
 * panel's queue, 3 tries with growing delays: a failed try is put back with
 * a delay instead of holding the worker, so other queued jobs keep running.
 * After the last try the channel keeps the error.
 *
 * The title and body may hold text from the agent or a site, so each
 * channel escapes them for its format.
 */
class PanelEvent extends Notification implements ShouldQueue
{
    use Queueable;

    /** Longer text is cut, under every provider's limit. */
    public const MAX_BODY = 1500;

    public int $tries = 3;

    public int $timeout = 30;

    public function __construct(
        public readonly string $type,
        public readonly string $title,
        public readonly string $body,
        public readonly bool $cleared = false,
        public ?int $channelId = null,
    ) {}

    /** @return list<int> seconds before the second and third try */
    public function backoff(): array
    {
        return [60, 300];
    }

    /** @return list<string> */
    public function via(NotificationChannel $notifiable): array
    {
        $this->channelId = $notifiable->id;

        return [$notifiable->type === NotificationChannel::EMAIL ? 'mail' : WebhookChannel::class];
    }

    /** The body cut to the providers' limits. */
    public function shortBody(): string
    {
        return mb_strlen($this->body) > self::MAX_BODY ? mb_substr($this->body, 0, self::MAX_BODY).'…' : $this->body;
    }

    /**
     * Mail through the owner's SMTP server, as a mailer made for this send.
     * The host is checked again here, right before the connection.
     */
    public function toMail(NotificationChannel $notifiable): MailMessage
    {
        $smtp = app(PanelSettings::class)->smtp();
        if (($smtp['host'] ?? '') === '') {
            throw new RuntimeException(__('zimam.notify.smtp.missing'));
        }
        if (($refusal = app(SmtpHost::class)->refusal($smtp['host'])) !== null) {
            throw new RuntimeException($refusal);
        }
        config(['mail.mailers.zimam-notify' => [
            'transport' => 'smtp',
            'scheme' => ($smtp['security'] ?? 'starttls') === 'tls' ? 'smtps' : 'smtp',
            'host' => $smtp['host'],
            'port' => (int) $smtp['port'],
            'username' => ($smtp['username'] ?? '') ?: null,
            'password' => ($smtp['password'] ?? '') ?: null,
            'timeout' => 10,
        ]]);

        return (new MailMessage)
            ->mailer('zimam-notify')
            ->from($smtp['from'])
            ->subject('[Zimam] '.$this->title)
            ->line($this->shortBody());
    }

    /** The last try failed: the channel shows why. */
    public function failed(Throwable $e): void
    {
        if ($this->channelId !== null) {
            NotificationChannel::whereKey($this->channelId)->update(['last_error' => mb_substr($e->getMessage(), 0, 1000)]);
        }
    }
}
