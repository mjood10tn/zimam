<?php

namespace App\Settings;

use App\Models\Setting;

/**
 * The panel's settings (§7.3 Settings, §7.6), with the defaults of §4 and
 * §11.4 until someone changes them. A default is never written back.
 */
class PanelSettings
{
    public const LOG_RETENTION_DAYS = 14;

    /** The audit log's own retention, a year (D24). */
    public const AUDIT_RETENTION_DAYS = 365;

    public const SITE_LOG_CAP_BYTES = 5 * 1024 * 1024 * 1024;

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
