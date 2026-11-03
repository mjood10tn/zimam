<?php

namespace App\Filament\Widgets;

use App\Agent\AgentHealth;
use App\Agent\ServerStatus;
use App\Models\Certificate;
use Carbon\CarbonImmutable;
use Filament\Widgets\Widget;

/**
 * The bulletin opening and the warnings in force (dashboard brief §3, §6):
 * a headline counting them, one sentence naming them, the issued time,
 * then one card per warning. Milestone 1 knows three: agent unreachable
 * (the headline and the banner), reboot required and security updates
 * pending (§7.5). Task 30 adds certificates near or past their end, the
 * first critical card, and so the critical-first order; Task 32 adds disks,
 * memory and services.
 */
class Warnings extends Widget
{
    protected static ?int $sort = 1;

    /** The triage view renders with the page, never after it. */
    protected static ?bool $isLazyLoaded = false;

    protected string $view = 'filament.widgets.warnings';

    /** @return array<string, mixed> */
    protected function getViewData(): array
    {
        $reachable = app(AgentHealth::class)->isReachable();
        $status = ServerStatus::current();
        $warnings = $status === null ? [] : $this->warnings($status);

        return [
            'headline' => $this->headline($reachable, $warnings),
            'lede' => $this->lede($status, $warnings),
            'status' => $status,
            'warnings' => $warnings,
        ];
    }

    /** @return list<array{severity: 'critical'|'warning', title: string, detail: string, at: string}> */
    protected function warnings(ServerStatus $status): array
    {
        $warnings = [];
        if ($status->data['reboot_required'] ?? false) {
            $warnings[] = [
                'severity' => 'warning',
                'title' => __('zimam.dashboard.warnings.reboot.title'),
                'detail' => __('zimam.dashboard.warnings.reboot.detail'),
                'at' => __('zimam.dashboard.checked', ['time' => self::time($status->issuedAt, 'H:i')]),
            ];
        }
        $security = $status->data['updates']['security'] ?? null;
        if (is_int($security) && $security > 0) {
            $checked = $status->data['updates']['checked_at'] ?? null;
            $warnings[] = [
                'severity' => 'warning',
                'title' => trans_choice('zimam.dashboard.warnings.security_updates.title', $security),
                'detail' => __('zimam.dashboard.warnings.security_updates.detail'),
                'at' => __('zimam.dashboard.checked', ['time' => self::time(is_string($checked) ? CarbonImmutable::parse($checked) : $status->issuedAt, 'H:i')]),
            ];
        }
        array_push($warnings, ...$this->certificateWarnings());
        // Critical first; otherwise in the order above.
        usort($warnings, fn (array $a, array $b): int => ($a['severity'] === 'critical' ? 0 : 1) <=> ($b['severity'] === 'critical' ? 0 : 1));

        return $warnings;
    }

    /**
     * Issued certificates within Certificate::WARN_DAYS of their end are a
     * warning, expired ones critical (§9.7's certificate_valid), soonest
     * first. The expiry is the panel's copy; security.check refreshes it.
     *
     * @return list<array{severity: 'critical'|'warning', title: string, detail: string, at: string}>
     */
    private function certificateWarnings(): array
    {
        $now = CarbonImmutable::now();
        $certificates = Certificate::query()
            ->with('site')
            ->where('status', Certificate::ISSUED)
            ->where('expires_at', '<=', $now->addDays(Certificate::WARN_DAYS))
            ->orderBy('expires_at')
            ->get();

        return $certificates->map(function (Certificate $certificate) use ($now): array {
            $expired = $certificate->expires_at->lte($now);
            $days = (int) max(1, ceil($now->diffInHours($certificate->expires_at) / 24));

            return [
                'severity' => $expired ? 'critical' : 'warning',
                'title' => $expired
                    ? __('zimam.dashboard.warnings.certificate_expired.title', ['site' => $certificate->site->name])
                    : trans_choice('zimam.dashboard.warnings.certificate_expiring.title', $days, ['site' => $certificate->site->name, 'days' => $days]),
                'detail' => __($expired ? 'zimam.dashboard.warnings.certificate_expired.detail' : 'zimam.dashboard.warnings.certificate_expiring.detail'),
                'at' => __('zimam.dashboard.expires', ['time' => self::time($certificate->expires_at, 'j M H:i')]),
            ];
        })->values()->all();
    }

    /** @param list<array{severity: string}> $warnings */
    protected function headline(bool $reachable, array $warnings): string
    {
        if (! $reachable) {
            return __('zimam.dashboard.headline.unreachable');
        }
        $critical = count(array_filter($warnings, fn (array $w): bool => $w['severity'] === 'critical'));
        $warning = count($warnings) - $critical;

        return match (true) {
            $critical > 0 && $warning > 0 => __('zimam.dashboard.headline.mixed', [
                'critical' => ucfirst(self::word($critical)),
                'warnings' => trans_choice('zimam.dashboard.headline.warnings_lower', $warning, ['number' => self::word($warning)]),
            ]),
            $critical > 0 => trans_choice('zimam.dashboard.headline.critical', $critical, ['number' => ucfirst(self::word($critical))]),
            $warning > 0 => trans_choice('zimam.dashboard.headline.warnings', $warning, ['number' => ucfirst(self::word($warning))]),
            default => __('zimam.dashboard.headline.none'),
        };
    }

    /** @param list<array{title: string}> $warnings */
    protected function lede(?ServerStatus $status, array $warnings): string
    {
        if ($status === null) {
            return __('zimam.dashboard.lede.no_report');
        }
        $sentence = $warnings === []
            ? __('zimam.dashboard.lede.none')
            : implode(' ', array_map(fn (array $w): string => $w['title'].'.', $warnings));

        return $status->fresh
            ? $sentence
            : __('zimam.dashboard.lede.cached', ['time' => self::time($status->issuedAt), 'sentence' => lcfirst($sentence)]);
    }

    /** Server time: seconds for the bulletin, which refreshes every 10 s; minutes for checks. */
    public static function time(CarbonImmutable $at, string $format = 'H:i:s'): string
    {
        return $at->setTimezone(config('app.timezone'))->format($format);
    }

    private static function word(int $number): string
    {
        return __('zimam.numbers')[$number] ?? (string) $number;
    }
}
