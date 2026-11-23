<?php

namespace App\Notifications\Channels;

use App\Models\NotificationChannel;
use App\Notifications\PanelEvent;
use Illuminate\Support\Facades\Http;
use RuntimeException;

/**
 * Sends a PanelEvent to Telegram, Slack or Discord (R05-D11). The hosts are
 * fixed: Telegram's API, and webhook URLs that passed the model's patterns
 * when saved and are checked again here. No redirect is followed and a send
 * gives up after 10 s, so the panel can't be turned into a request tool. Text
 * is escaped for each format, and Discord pings nobody.
 */
final class WebhookChannel
{
    public const TIMEOUT_SECONDS = 10;

    public function send(NotificationChannel $notifiable, PanelEvent $event): void
    {
        $s = $notifiable->settings;
        [$url, $payload] = match ($notifiable->type) {
            NotificationChannel::TELEGRAM => self::telegram($s, $event),
            NotificationChannel::SLACK => [self::checked($s['webhook_url'] ?? '', NotificationChannel::SLACK_URL_PATTERN), ['text' => '*'.self::slack($event->title)."*\n".self::slack($event->shortBody())]],
            NotificationChannel::DISCORD => [self::checked($s['webhook_url'] ?? '', NotificationChannel::DISCORD_URL_PATTERN), [
                'content' => '**'.self::discord($event->title)."**\n".self::discord($event->shortBody()),
                'allowed_mentions' => ['parse' => []],
            ]],
            default => throw new RuntimeException("Not a webhook channel: {$notifiable->type}"),
        };
        $response = Http::withOptions(['allow_redirects' => false])
            ->timeout(self::TIMEOUT_SECONDS)
            ->connectTimeout(self::TIMEOUT_SECONDS)
            ->asJson()
            ->post($url, $payload);
        if (! $response->successful()) {
            throw new RuntimeException(__('zimam.notify.http_failed', ['status' => $response->status()]));
        }
        $notifiable->update(['last_error' => null, 'last_sent_at' => now()]);
    }

    /**
     * @param  array<string, string>  $s
     * @return array{string, array<string, mixed>}
     */
    private static function telegram(array $s, PanelEvent $event): array
    {
        $token = self::checked($s['bot_token'] ?? '', NotificationChannel::TELEGRAM_TOKEN_PATTERN);

        return ["https://api.telegram.org/bot{$token}/sendMessage", [
            'chat_id' => $s['chat_id'] ?? '',
            'text' => '<b>'.self::html($event->title)."</b>\n".self::html($event->shortBody()),
            'parse_mode' => 'HTML',
            'disable_web_page_preview' => true,
        ]];
    }

    private static function checked(string $value, string $pattern): string
    {
        if (preg_match($pattern, $value) !== 1) {
            throw new RuntimeException(__('zimam.notify.bad_target'));
        }

        return $value;
    }

    /** Telegram's HTML mode escapes only <, > and &. */
    private static function html(string $text): string
    {
        return htmlspecialchars($text, ENT_NOQUOTES | ENT_SUBSTITUTE, 'UTF-8');
    }

    /** Slack's mrkdwn: &, < and > are its control characters. */
    private static function slack(string $text): string
    {
        return str_replace(['&', '<', '>'], ['&amp;', '&lt;', '&gt;'], $text);
    }

    /** Discord markdown characters lose their meaning behind a backslash. */
    private static function discord(string $text): string
    {
        return preg_replace('/([\\\\*_~`|>#\[\]])/', '\\\\$1', $text);
    }
}
