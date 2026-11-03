<?php

namespace App\Filament\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\LogsPolicy;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Models\AgentJob;
use App\Settings\PanelSettings;
use App\Support\IpEntry;
use BackedEnum;
use Closure;
use Filament\Forms\Components\Checkbox;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Filament\Support\Icons\Heroicon;
use Symfony\Component\HttpFoundation\IpUtils;

/**
 * Log retention, the audit log's retention and the per-site cap, sent to
 * the agent as logs.policy and stored once it applies them; and the panel
 * IP allowlist, kept in the panel only (§7.2, §7.3, §11.4, D24).
 */
class Settings extends Page
{
    public const RECOVERY_COMMAND = 'sudo zimam-agent panel setup-link --clear-ip-allowlist';

    private const GIB = 1024 ** 3;

    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedCog6Tooth;

    protected static ?int $navigationSort = 9;

    protected string $view = 'filament.pages.settings';

    /** @var array<string, mixed>|null */
    public ?array $data = [];

    /** Set once a save found the new allowlist would leave out the current address. */
    public bool $lockoutPending = false;

    public static function getNavigationLabel(): string
    {
        return __('zimam.settings.title');
    }

    public function getTitle(): string
    {
        return __('zimam.settings.title');
    }

    public function mount(): void
    {
        $s = app(PanelSettings::class);
        $this->form->fill([
            'log_retention_days' => $s->logRetentionDays(),
            'audit_retention_days' => $s->auditRetentionDays(),
            'site_log_cap_gib' => intdiv($s->siteLogCapBytes(), self::GIB),
            'ip_allowlist' => implode("\n", $s->panelIpAllowlist()),
            'confirm_lockout' => false,
        ]);
    }

    public function form(Schema $schema): Schema
    {
        return $schema->statePath('data')->components([
            Section::make(__('zimam.settings.logs'))->schema([
                TextInput::make('log_retention_days')->label(__('zimam.settings.log_retention_days'))
                    ->helperText(__('zimam.settings.help.log_retention_days'))
                    ->required()->integer()->minValue(1)->maxValue(365)->suffix(__('zimam.settings.days')),
                TextInput::make('audit_retention_days')->label(__('zimam.settings.audit_retention_days'))
                    ->helperText(__('zimam.settings.help.audit_retention_days'))
                    ->required()->integer()->minValue(1)->maxValue(3650)->suffix(__('zimam.settings.days')),
                TextInput::make('site_log_cap_gib')->label(__('zimam.settings.site_log_cap'))
                    ->helperText(__('zimam.settings.help.site_log_cap'))
                    ->required()->integer()->minValue(1)->maxValue(100)->suffix('GiB'),
            ]),
            Section::make(__('zimam.settings.access'))->schema([
                Textarea::make('ip_allowlist')->label(__('zimam.settings.ip_allowlist'))
                    ->helperText(__('zimam.settings.help.ip_allowlist'))
                    ->rows(4)
                    ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                        $lines = IpEntry::lines(is_string($value) ? $value : '');
                        if (count($lines) > IpEntry::MAX_ENTRIES) {
                            $fail(__('zimam.security.validation.too_many', ['max' => IpEntry::MAX_ENTRIES]));
                        }
                        foreach ($lines as $i => $line) {
                            if (($error = IpEntry::error($line)) !== null) {
                                $fail(__('zimam.security.validation.line', ['line' => $i + 1, 'entry' => $line, 'error' => $error]));

                                return;
                            }
                        }
                    }),
                Checkbox::make('confirm_lockout')
                    ->label(__('zimam.settings.confirm_lockout', ['command' => self::RECOVERY_COMMAND]))
                    ->visible(fn (): bool => $this->lockoutPending),
            ]),
        ]);
    }

    public function save(): void
    {
        $state = $this->form->getState();
        $settings = app(PanelSettings::class);

        $entries = IpEntry::lines($state['ip_allowlist'] ?? '');
        $ip = (string) request()->getClientIp();
        if ($entries !== [] && ! IpUtils::checkIp($ip, $entries) && ! ($state['confirm_lockout'] ?? false)) {
            $this->lockoutPending = true;
            $this->addError('data.confirm_lockout', __('zimam.settings.lockout', ['ip' => $ip, 'command' => self::RECOVERY_COMMAND]));

            return;
        }
        if ($entries !== $settings->panelIpAllowlist()) {
            $settings->setPanelIpAllowlist($entries);
            Notification::make()->title(__('zimam.settings.allowlist_saved'))->success()->send();
        }
        $this->lockoutPending = false;

        $policy = new LogsPolicy((int) $state['log_retention_days'], (int) $state['audit_retention_days'], (int) $state['site_log_cap_gib'] * self::GIB);
        $current = [$settings->logRetentionDays(), $settings->auditRetentionDays(), $settings->siteLogCapBytes()];
        if ([$policy->retentionDays, $policy->auditRetentionDays, $policy->siteCapBytes] === $current) {
            return;
        }
        if (! app(AgentHealth::class)->isReachable()) {
            Notification::make()->title(__('zimam.agent.paused'))->warning()->send();

            return;
        }
        if (AgentJob::query()->where('command', 'logs.policy')->whereNotIn('status', AgentJob::FINISHED)->exists()) {
            Notification::make()->title(__('zimam.settings.policy_pending'))->warning()->send();

            return;
        }
        try {
            app(SubmitAgentCommand::class)->run($policy);
        } catch (Halt) {
            return;
        } catch (AgentValidationFailed $e) {
            foreach ($e->fields() as $field => $message) {
                $this->addError('data.'.match ($field) {
                    'retention_days' => 'log_retention_days',
                    'site_cap_bytes' => 'site_log_cap_gib',
                    default => $field,
                }, $message);
            }

            return;
        } catch (AgentException $e) {
            Notification::make()->title($e->getMessage())->danger()->send();

            return;
        }
        Notification::make()->title(__('zimam.settings.policy_submitted'))->body(__('zimam.php.submitted_body'))->success()->send();
    }
}
