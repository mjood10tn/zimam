<?php

namespace App\Filament\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Exceptions\AgentException;
use App\Console\Commands\RunSecurityChecks;
use App\Models\Certificate;
use App\Models\SecurityFinding;
use App\Models\Site;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Support\Exceptions\Halt;
use Filament\Support\Icons\Heroicon;

/**
 * Every site's HTTPS state, certificate expiry, active protections and
 * failing checks, then the server's checks; "Run checks now" (§7.3, §9.7).
 */
class Security extends Page
{
    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedShieldCheck;

    protected static ?int $navigationSort = 2;

    protected string $view = 'filament.pages.security';

    public static function getNavigationLabel(): string
    {
        return __('zimam.checks.title');
    }

    public function getTitle(): string
    {
        return __('zimam.checks.title');
    }

    protected function getHeaderActions(): array
    {
        return [
            AgentHealth::guard(
                Action::make('runChecks')
                    ->label(__('zimam.checks.run'))
                    ->action(fn () => $this->runChecks()),
            ),
        ];
    }

    /** Submits the server's checks and every active site's; the outcomes store the findings. */
    private function runChecks(): void
    {
        foreach (RunSecurityChecks::commands() as $command) {
            try {
                app(SubmitAgentCommand::class)->run($command, $command->site === null ? null : Site::query()->where('name', $command->site)->first());
            } catch (Halt) {
                return;
            } catch (AgentException $e) {
                Notification::make()->title($e->getMessage())->danger()->send();

                return;
            }
        }
        Notification::make()->title(__('zimam.checks.submitted'))->body(__('zimam.php.submitted_body'))->success()->send();
    }

    /** @return array<string, mixed> */
    protected function getViewData(): array
    {
        $sites = Site::query()->with(['securitySettings', 'certificate', 'securityFindings'])->orderBy('name')->get();

        return [
            'sites' => $sites->map(fn (Site $site): array => $this->siteRow($site))->all(),
            'server' => SecurityFinding::query()->where('scope', 'server')->get()
                ->sortBy(fn (SecurityFinding $f): int|false => array_search($f->check, SecurityFinding::SERVER_CHECKS, true))
                ->values()->all(),
        ];
    }

    /** @return array<string, mixed> */
    private function siteRow(Site $site): array
    {
        $settings = $site->securitySettings;
        $certificate = $site->certificate?->status === Certificate::ISSUED ? $site->certificate : null;
        $protections = array_keys(array_filter([
            'headers_preset' => $settings?->headers_preset,
            'hsts' => $settings?->hsts_max_age !== null,
            'block_risky_functions' => $settings?->block_risky_functions,
            'restrict_to_site_folder' => $settings?->restrict_to_site_folder,
            'ip_allow' => $settings?->ip_allow !== [],
            'ip_deny' => $settings?->ip_deny !== [],
            'password_protection' => $settings?->password_protection,
        ]));

        return [
            'name' => $site->name,
            'https' => match (true) {
                $certificate === null => 'none',
                (bool) $settings?->force_https => 'forced',
                default => 'available',
            },
            'expires' => $certificate?->expires_at,
            'expires_soon' => $certificate?->expires_at !== null && $certificate->expires_at->lte(now()->addDays(Certificate::WARN_DAYS)),
            'protections' => $protections,
            'failing' => $site->securityFindings
                ->where('status', '!=', SecurityFinding::PASS)
                ->sortBy(fn (SecurityFinding $f): int|false => array_search($f->check, SecurityFinding::SITE_CHECKS, true))
                ->values()->all(),
            'checked' => $site->securityFindings->max('checked_at'),
        ];
    }
}
