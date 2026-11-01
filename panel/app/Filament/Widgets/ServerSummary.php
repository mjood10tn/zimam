<?php

namespace App\Filament\Widgets;

use App\Agent\ServerStatus;
use Carbon\CarbonImmutable;
use Filament\Widgets\Widget;

/** The server facts from system.status (§7.3, §7.5), in fixed order. */
class ServerSummary extends Widget
{
    protected static ?int $sort = 2;

    protected static ?bool $isLazyLoaded = false;

    protected string $view = 'filament.widgets.server-summary';

    private const LETS_ENCRYPT = 'https://acme-v02.api.letsencrypt.org/directory';

    private const LETS_ENCRYPT_STAGING = 'https://acme-staging-v02.api.letsencrypt.org/directory';

    /** @return array<string, mixed> */
    protected function getViewData(): array
    {
        $status = ServerStatus::current();

        return [
            'status' => $status,
            'rows' => $status === null ? [] : $this->rows($status->data),
        ];
    }

    /**
     * @param  array<string, mixed>  $data
     * @return list<array{string, string}>
     */
    protected function rows(array $data): array
    {
        return [
            [__('zimam.server.agent'), __('zimam.server.agent_value', [
                'version' => $data['agent_version'] ?? '?',
                'protocols' => implode(', ', (array) ($data['protocols'] ?? [])),
            ])],
            [__('zimam.server.nginx'), filled($data['nginx_version'] ?? null) ? $data['nginx_version'] : __('zimam.server.nginx_unknown')],
            [__('zimam.server.php'), $this->php($data['php'] ?? [])],
            [__('zimam.server.panel_domain'), (string) ($data['panel_domain'] ?? '')],
            [__('zimam.server.acme'), match ($data['acme_directory'] ?? null) {
                self::LETS_ENCRYPT => __('zimam.server.acme_production'),
                self::LETS_ENCRYPT_STAGING => __('zimam.server.acme_staging'),
                default => (string) ($data['acme_directory'] ?? ''),
            }],
            [__('zimam.server.reboot'), ($data['reboot_required'] ?? false) ? __('zimam.server.reboot_required') : __('zimam.server.reboot_not_required')],
            [__('zimam.server.updates'), $this->updates($data['updates'] ?? [])],
        ];
    }

    /** @param array<string, mixed> $php */
    private function php(array $php): string
    {
        $versions = array_column($php['installed'] ?? [], 'version');
        if ($versions === []) {
            return __('zimam.server.php_none');
        }
        usort($versions, 'version_compare');

        return implode(', ', array_map(function (string $version) use ($php): string {
            $default = $version === ($php['default'] ?? null);
            $panel = $version === ($php['panel'] ?? null);

            return match (true) {
                $default && $panel => __('zimam.server.php_default_panel', ['version' => $version]),
                $default => __('zimam.server.php_default', ['version' => $version]),
                $panel => __('zimam.server.php_panel', ['version' => $version]),
                default => $version,
            };
        }, $versions));
    }

    /** @param array<string, mixed> $updates */
    private function updates(array $updates): string
    {
        $total = $updates['total'] ?? null;
        $checked = $updates['checked_at'] ?? null;
        if (! is_int($total) || ! is_string($checked)) {
            return __('zimam.server.updates_unknown');
        }
        $time = Warnings::time(CarbonImmutable::parse($checked), 'H:i');

        return $total === 0
            ? __('zimam.server.updates_none', ['time' => $time])
            : __('zimam.server.updates_value', ['total' => $total, 'security' => (int) ($updates['security'] ?? 0), 'time' => $time]);
    }
}
