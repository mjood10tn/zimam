<?php

namespace App\Settings;

use App\Models\Setting;
use Illuminate\Support\Facades\Crypt;
use SensitiveParameter;

/**
 * The panel's settings (§7.3 Settings, §7.6), with the defaults of §4 and
 * §11.4 until someone changes them. A default is never written back.
 */
class PanelSettings
{
    public const LOG_RETENTION_DAYS = 14;

    /** The audit log's own retention, a year (D24). */
    public const AUDIT_RETENTION_DAYS = 365;

    public const DISPLAY_TIMEZONE = 'UTC';

    public const SITE_LOG_CAP_BYTES = 5 * 1024 * 1024 * 1024;

    /** sshd's own defaults on Ubuntu 26.04, with root by key only. */
    public const SSH_DEFAULTS = ['password_auth' => true, 'root_login' => 'prohibit-password', 'ports' => [22], 'max_auth_tries' => 6, 'login_grace_seconds' => 120];

    /** The display zone, read once per request: Filament asks for it per cell. */
    private ?string $zone = null;

    public function logRetentionDays(): int
    {
        return (int) $this->get('log_retention_days', self::LOG_RETENTION_DAYS);
    }

    public function auditRetentionDays(): int
    {
        return (int) $this->get('audit_retention_days', self::AUDIT_RETENTION_DAYS);
    }

    /** Stores the log policy the agent has applied (logs.policy's outcome). */
    public function setLogPolicy(int $retentionDays, int $auditRetentionDays, int $siteLogCapBytes): void
    {
        $this->set('log_retention_days', $retentionDays);
        $this->set('audit_retention_days', $auditRetentionDays);
        $this->set('site_log_cap_bytes', $siteLogCapBytes);
    }

    public function siteLogCapBytes(): int
    {
        return (int) $this->get('site_log_cap_bytes', self::SITE_LOG_CAP_BYTES);
    }

    /** @return list<string> addresses and CIDR prefixes; empty allows every address */
    public function panelIpAllowlist(): array
    {
        return array_values(array_map('strval', (array) $this->get('panel_ip_allowlist', [])));
    }

    /** @param list<string> $entries */
    public function setPanelIpAllowlist(array $entries): void
    {
        $this->set('panel_ip_allowlist', array_values($entries));
    }

    public function displayTimezone(): string
    {
        if ($this->zone !== null) {
            return $this->zone;
        }
        $zone = (string) $this->get('display_timezone', self::DISPLAY_TIMEZONE);

        return $this->zone = in_array($zone, \DateTimeZone::listIdentifiers(), true) ? $zone : self::DISPLAY_TIMEZONE;
    }

    /** The zone the panel shows times in; the panel keeps storing UTC. */
    public function setDisplayTimezone(string $zone): void
    {
        if (! in_array($zone, \DateTimeZone::listIdentifiers(), true)) {
            throw new \InvalidArgumentException('Unknown time zone.');
        }
        $this->set('display_timezone', $zone);
        $this->zone = null;
    }

    /**
     * The SMTP server notifications go through (R05-D11): host, port,
     * security (starttls or tls), username, password and from. Empty until
     * the owner sets it. The password is stored encrypted.
     *
     * @return array<string, mixed>
     */
    public function smtp(): array
    {
        $smtp = (array) $this->get('smtp', []);
        if (($smtp['password'] ?? '') !== '') {
            $smtp['password'] = Crypt::decryptString($smtp['password']);
        }

        return $smtp;
    }

    /** @param array<string, mixed> $smtp */
    public function setSmtp(#[SensitiveParameter] array $smtp): void
    {
        if (($smtp['password'] ?? '') !== '') {
            $smtp['password'] = Crypt::encryptString($smtp['password']);
        }
        $this->set('smtp', $smtp);
    }

    /**
     * The SSH settings last kept (R06-D4): what the SSH form shows. Until the
     * owner keeps a change, Ubuntu's defaults.
     *
     * shortcut: these are what the panel last applied, not sshd -T's live
     * values; read them from the agent once a read command reports them.
     *
     * @return array{password_auth: bool, root_login: string, ports: list<int>, max_auth_tries: int, login_grace_seconds: int}
     */
    public function ssh(): array
    {
        return (array) $this->get('ssh_settings', []) + self::SSH_DEFAULTS;
    }

    /** @param array<string, mixed> $settings ssh.settings's params, once kept */
    public function setSsh(array $settings): void
    {
        $this->set('ssh_settings', array_intersect_key($settings, self::SSH_DEFAULTS));
    }

    private function get(string $key, mixed $default): mixed
    {
        $setting = Setting::query()->where('key', $key)->first();

        return $setting === null ? $default : $setting->value;
    }

    private function set(string $key, mixed $value): void
    {
        Setting::query()->updateOrCreate(['key' => $key], ['value' => $value]);
    }
}
