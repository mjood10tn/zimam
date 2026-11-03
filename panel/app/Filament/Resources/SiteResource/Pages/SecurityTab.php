<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\SiteSecurity;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Models\Certificate;
use App\Models\Site;
use App\Models\SiteBasicAuthUser;
use App\Support\IpEntry;
use Closure;
use Filament\Forms\Components\Hidden;
use Filament\Forms\Components\Repeater;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Illuminate\Contracts\View\View;
use Illuminate\Support\Facades\DB;
use Livewire\Component;

/**
 * A site's Security tab (§7.3, §9.6): a form for every setting. Saving
 * submits the whole set as one site.security job, and the outcome stores
 * it. Passwords are hashed here with bcrypt at cost 12, and only the hashes
 * are kept or sent (§5.4).
 */
class SecurityTab extends Component implements HasSchemas
{
    use InteractsWithSchemas;

    public Site $site;

    /** @var array<string, mixed>|null */
    public ?array $data = [];

    public function mount(): void
    {
        $settings = $this->site->securitySettings()->firstOrCreate();
        $this->form->fill([
            'force_https' => $settings->force_https,
            'hsts_max_age' => $settings->hsts_max_age === null ? null : (string) $settings->hsts_max_age,
            'hsts_include_subdomains' => $settings->hsts_include_subdomains,
            'headers_preset' => $settings->headers_preset,
            'block_risky_functions' => $settings->block_risky_functions,
            'restrict_to_site_folder' => $settings->restrict_to_site_folder,
            'ip_allow' => implode("\n", $settings->ip_allow),
            'ip_deny' => implode("\n", $settings->ip_deny),
            'password_protection' => $settings->password_protection,
            'users' => $this->site->basicAuthUsers()->orderBy('username')->get()
                ->map(fn (SiteBasicAuthUser $u): array => ['id' => $u->id, 'username' => $u->username, 'password' => null])
                ->all(),
        ]);
    }

    public function form(Schema $schema): Schema
    {
        return $schema
            ->statePath('data')
            ->components([
                Section::make(__('zimam.security.sections.https'))->schema([
                    Toggle::make('force_https')
                        ->label(__('zimam.security.fields.force_https'))
                        ->helperText(fn (): string => $this->hasCertificate() ? __('zimam.security.help.force_https') : __('zimam.security.help.force_https_no_certificate'))
                        ->disabled(fn (): bool => ! $this->hasCertificate()),
                    Select::make('hsts_max_age')
                        ->label(__('zimam.security.fields.hsts'))
                        ->helperText(__('zimam.security.help.hsts'))
                        ->placeholder(__('zimam.security.hsts.off'))
                        ->options([
                            '86400' => __('zimam.security.hsts.day'),
                            '2592000' => __('zimam.security.hsts.month'),
                            '31536000' => __('zimam.security.hsts.year'),
                        ])
                        ->live(),
                    Toggle::make('hsts_include_subdomains')
                        ->label(__('zimam.security.fields.hsts_include_subdomains'))
                        ->disabled(fn (Get $get): bool => blank($get('hsts_max_age'))),
                    Toggle::make('headers_preset')
                        ->label(__('zimam.security.fields.headers_preset'))
                        ->helperText(__('zimam.security.help.headers_preset')),
                ]),
                Section::make(__('zimam.security.sections.php'))
                    ->visible(fn (): bool => $this->site->type === Site::PHP)
                    ->schema([
                        Toggle::make('block_risky_functions')
                            ->label(__('zimam.security.fields.block_risky_functions'))
                            ->helperText(__('zimam.security.help.block_risky_functions')),
                        Toggle::make('restrict_to_site_folder')
                            ->label(__('zimam.security.fields.restrict_to_site_folder'))
                            ->helperText(__('zimam.security.help.restrict_to_site_folder')),
                    ]),
                Section::make(__('zimam.security.sections.access'))->schema([
                    Textarea::make('ip_allow')
                        ->label(__('zimam.security.fields.ip_allow'))
                        ->helperText(__('zimam.security.help.ip_allow'))
                        ->rows(3)
                        ->rule(self::ipListRule()),
                    Textarea::make('ip_deny')
                        ->label(__('zimam.security.fields.ip_deny'))
                        ->helperText(__('zimam.security.help.ip_deny'))
                        ->rows(3)
                        ->rule(self::ipListRule()),
                    Toggle::make('password_protection')
                        ->label(__('zimam.security.fields.password_protection'))
                        ->helperText(__('zimam.security.help.password_protection'))
                        ->live(),
                    Repeater::make('users')
                        ->label(__('zimam.security.fields.users'))
                        ->visible(fn (Get $get): bool => (bool) $get('password_protection'))
                        ->required(fn (Get $get): bool => (bool) $get('password_protection'))
                        ->maxItems(SiteBasicAuthUser::MAX_USERS)
                        ->addActionLabel(__('zimam.security.users.add'))
                        ->columns(2)
                        ->schema([
                            Hidden::make('id'),
                            TextInput::make('username')
                                ->label(__('zimam.security.users.username'))
                                ->required()
                                ->regex(SiteBasicAuthUser::USERNAME_PATTERN)
                                ->validationMessages(['regex' => __('zimam.security.validation.username')])
                                ->distinct(),
                            TextInput::make('password')
                                ->label(__('zimam.security.users.password'))
                                ->password()
                                ->revealable()
                                ->maxLength(72) // bcrypt reads no further
                                ->required(fn (Get $get): bool => blank($get('id')))
                                ->helperText(fn (Get $get): ?string => blank($get('id')) ? null : __('zimam.security.users.keep')),
                        ]),
                ]),
            ]);
    }

    /** Each line an entry the agent takes (§5.4), at most 100. */
    private static function ipListRule(): Closure
    {
        return function (string $attribute, mixed $value, Closure $fail): void {
            $lines = IpEntry::lines(is_string($value) ? $value : '');
            if (count($lines) > IpEntry::MAX_ENTRIES) {
                $fail(__('zimam.security.validation.too_many', ['max' => IpEntry::MAX_ENTRIES]));

                return;
            }
            foreach ($lines as $i => $line) {
                if (($error = IpEntry::error($line)) !== null) {
                    $fail(__('zimam.security.validation.line', ['line' => $i + 1, 'entry' => $line, 'error' => $error]));

                    return;
                }
            }
        };
    }

    private function hasCertificate(): bool
    {
        return $this->site->certificate?->status === Certificate::ISSUED;
    }

    public function save(): void
    {
        if (! app(AgentHealth::class)->isReachable()) {
            Notification::make()->title(__('zimam.agent.paused'))->warning()->send();

            return;
        }
        if ($this->site->status !== Site::ACTIVE || $this->site->hasUnfinishedJob()) {
            Notification::make()->title(__('zimam.sites.busy'))->warning()->send();

            return;
        }
        $state = $this->form->getState();
        $protect = (bool) ($state['password_protection'] ?? false);
        $users = $protect ? $this->hashUsers($state['users'] ?? []) : [];
        $command = new SiteSecurity(
            site: $this->site->name,
            forceHttps: $this->hasCertificate() && (bool) ($state['force_https'] ?? false),
            hstsMaxAge: filled($state['hsts_max_age'] ?? null) ? (int) $state['hsts_max_age'] : null,
            hstsIncludeSubdomains: filled($state['hsts_max_age'] ?? null) && (bool) ($state['hsts_include_subdomains'] ?? false),
            headersPreset: (bool) ($state['headers_preset'] ?? false),
            blockRiskyFunctions: (bool) ($state['block_risky_functions'] ?? false),
            restrictToSiteFolder: (bool) ($state['restrict_to_site_folder'] ?? false),
            ipAllow: IpEntry::lines($state['ip_allow'] ?? ''),
            ipDeny: IpEntry::lines($state['ip_deny'] ?? ''),
            passwordProtection: $protect,
            users: $users,
        );
        try {
            app(SubmitAgentCommand::class)->run($command, $this->site);
        } catch (Halt) {
            return;
        } catch (AgentValidationFailed $e) {
            foreach ($e->fields() as $field => $message) {
                $this->addError('data.'.match (true) {
                    str_starts_with($field, 'password_protection') => 'users',
                    str_starts_with($field, 'hsts') => 'hsts_max_age',
                    default => $field,
                }, $message);
            }

            return;
        } catch (AgentException $e) {
            Notification::make()->title($e->getMessage())->danger()->send();

            return;
        }
        if ($protect) {
            $this->storeUsers($users);
        }
        $this->site->update(['last_error' => null]);
        Notification::make()->title(__('zimam.security.submitted', ['name' => $this->site->name]))->body(__('zimam.php.submitted_body'))->success()->send();
    }

    /**
     * Each user with the hash the agent gets: a new password hashed now, or
     * the stored hash of a user whose password was left empty.
     *
     * @param  array<array-key, array{id?: mixed, username: string, password?: ?string}>  $items
     * @return list<array{username: string, password_hash: string}>
     */
    private function hashUsers(array $items): array
    {
        $stored = $this->site->basicAuthUsers()->get()->keyBy('id');
        $users = [];
        foreach (array_values($items) as $item) {
            $hash = filled($item['password'] ?? null)
                ? password_hash($item['password'], PASSWORD_BCRYPT, ['cost' => SiteBasicAuthUser::BCRYPT_COST])
                : $stored->get($item['id'] ?? null)?->password_hash;
            if ($hash === null) {
                throw new Halt; // the form requires a password for a new user
            }
            $users[] = ['username' => $item['username'], 'password_hash' => $hash];
        }

        return $users;
    }

    /** @param list<array{username: string, password_hash: string}> $users */
    private function storeUsers(array $users): void
    {
        DB::transaction(function () use ($users): void {
            $this->site->basicAuthUsers()->delete();
            $this->site->basicAuthUsers()->createMany($users);
        });
        $this->mount();
    }

    public function render(): View
    {
        return view('filament.sites.security-tab');
    }
}
