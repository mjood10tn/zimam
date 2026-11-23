<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Notifications\Notifiable;

/**
 * Where the panel sends notifications (R05-D11): email through the panel's
 * SMTP settings, or Telegram, Slack or Discord at their fixed hosts. Each
 * channel picks its events. The settings are encrypted at rest.
 *
 * @property array<string, string> $settings
 * @property list<string> $events
 */
class NotificationChannel extends Model
{
    use Notifiable;

    public const EMAIL = 'email';

    public const TELEGRAM = 'telegram';

    public const SLACK = 'slack';

    public const DISCORD = 'discord';

    public const TYPES = [self::EMAIL, self::TELEGRAM, self::SLACK, self::DISCORD];

    /** The events a channel can get (R05-D12). */
    public const EVENTS = ['monitoring', 'workers', 'agent', 'job_failed', 'deploy', 'backup', 'certificate', 'security'];

    /** Only Slack's incoming-webhook host: the panel never posts anywhere else for Slack. */
    public const SLACK_URL_PATTERN = '/^https:\/\/hooks\.slack\.com\/services\/[A-Za-z0-9\/_-]+\z/';

    public const DISCORD_URL_PATTERN = '/^https:\/\/(discord|discordapp)\.com\/api\/webhooks\/[0-9]+\/[A-Za-z0-9_-]+\z/';

    public const TELEGRAM_TOKEN_PATTERN = '/^[0-9]{5,12}:[A-Za-z0-9_-]{30,64}\z/';

    public const TELEGRAM_CHAT_PATTERN = '/^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})\z/';

    protected $fillable = ['type', 'name', 'settings', 'events', 'enabled', 'last_error', 'last_sent_at'];

    protected function casts(): array
    {
        return ['settings' => 'encrypted:array', 'events' => 'array', 'enabled' => 'boolean', 'last_sent_at' => 'datetime'];
    }

    public function routeNotificationForMail(): string
    {
        return $this->settings['to'] ?? '';
    }
}
