<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\StartDeployment;
use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\AgentHealth;
use App\Agent\Commands\SiteReleases;
use App\Agent\Commands\SiteRepo;
use App\Agent\Commands\SiteRollback;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\Deployment;
use App\Models\Site;
use App\Models\SiteRepository;
use Closure;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Textarea;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Illuminate\Contracts\View\View;
use Illuminate\Database\Eloquent\Collection;
use Livewire\Attributes\Locked;
use Livewire\Component;

/**
 * A site's Deployments tab (R02-D2–D12): the repository and branch
 * (site.repo), the deploy key, the webhook and deploy URL, the deploy
 * settings, Deploy now, the deployments with their live log, and the kept
 * releases with Roll back. The webhook secret and deploy token are shown
 * once, when made; regenerating them, rolling back and changing the deploy
 * script ask for the password.
 */
class DeploymentsTab extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    /** The deployments listed, newest first. */
    public const LISTED = 20;

    public Site $site;

    /** @var array<string, mixed>|null */
    public ?array $repositoryData = [];

    /** @var array<string, mixed>|null */
    public ?array $settingsData = [];

    /**
     * The webhook secret and the deploy URL's token, only in the response of
     * the request that made them. They are private, so they never enter the
     * snapshot that rides along on every later request; the view shows them
     * in a wire:ignore block keyed by the reveal keys below, which the
     * browser keeps through later morphs until the page is left.
     */
    private ?string $revealedWebhookSecret = null;

    private ?string $revealedDeployToken = null;

    /** Changes with each reveal, so the browser swaps in the new value's block. Only the server sets it. */
    #[Locked]
    public ?string $webhookRevealKey = null;

    #[Locked]
    public ?string $deployUrlRevealKey = null;

    /** The deployment whose log is shown; the newest when null. */
    public ?int $logDeployment = null;

    /**
     * site.releases's last answer, kept between requests so the 5-second
     * poll during a job doesn't ask again; null when it couldn't be read.
     * Locked: only the server sets it, and Roll back reads the agent again
     * before it trusts any release.
     *
     * @var array<string, mixed>|null
     */
    #[Locked]
    public ?array $releasesData = null;

    #[Locked]
    public ?string $releasesError = null;

    /**
     * The site's job history when the releases were read: its newest job and
     * when that one finished. Any job started or ended since, from this tab
     * or not (a webhook, the deploy URL, another tab), changes it, and the
     * next render reads again. A running job doesn't change it, so the poll
     * doesn't.
     */
    #[Locked]
    public ?string $releasesMark = null;

    private function reveal(?string $webhookSecret = null, ?string $deployToken = null): void
    {
        if ($webhookSecret !== null) {
            $this->revealedWebhookSecret = $webhookSecret;
            $this->webhookRevealKey = bin2hex(random_bytes(8));
        }
        if ($deployToken !== null) {
            $this->revealedDeployToken = $deployToken;
            $this->deployUrlRevealKey = bin2hex(random_bytes(8));
        }
    }

    public function mount(): void
    {
        $repository = $this->site->repository;
        $this->repositoryForm->fill([
            'repository' => $repository?->repository,
            'branch' => $repository?->branch ?? 'main',
            'rotate_key' => false,
        ]);
        if ($repository !== null) {
            $this->fillSettings($repository);
        }
    }

    private function fillSettings(SiteRepository $repository): void
    {
        $this->settingsForm->fill([
            'auto_deploy' => $repository->auto_deploy,
            'deploy_script' => $repository->deploy_script,
            'run_composer' => $repository->run_composer,
            'keep_releases' => $repository->keep_releases,
        ]);
    }

    public function repositoryForm(Schema $schema): Schema
    {
        return $schema
            ->statePath('repositoryData')
            ->components([
                TextInput::make('repository')
                    ->label(__('zimam.deployments.fields.repository'))
                    ->helperText(__('zimam.deployments.help.repository'))
                    ->placeholder(__('zimam.deployments.fields.repository_placeholder'))
                    ->required()
                    ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                        if (! is_string($value) || ! SiteRepository::isValidRepository($value)) {
                            $fail(__('zimam.deployments.validation.repository'));
                        }
                    }),
                TextInput::make('branch')
                    ->label(__('zimam.deployments.fields.branch'))
                    ->required()
                    ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                        if (! is_string($value) || ! SiteRepository::isValidBranch($value)) {
                            $fail(__('zimam.deployments.validation.branch'));
                        }
                    }),
                Toggle::make('rotate_key')
                    ->label(__('zimam.deployments.fields.rotate_key'))
                    ->helperText(__('zimam.deployments.help.rotate_key'))
                    ->visible(fn (): bool => $this->repository()?->isConnected() ?? false),
            ]);
    }

    public function settingsForm(Schema $schema): Schema
    {
        return $schema
            ->statePath('settingsData')
            ->components([
                Toggle::make('auto_deploy')
                    ->label(__('zimam.deployments.settings.auto_deploy'))
                    ->helperText(__('zimam.deployments.settings.auto_deploy_help')),
                Textarea::make('deploy_script')
                    ->label(__('zimam.deployments.settings.script'))
                    ->helperText(__('zimam.deployments.settings.script_help').' '.__('zimam.deployments.settings.script_node'))
                    ->rows(8)
                    ->extraInputAttributes(['class' => 'font-mono', 'spellcheck' => 'false'])
                    // The limit is in bytes, as the agent counts it, so no
                    // maxLength: a browser counts characters.
                    ->rule(fn (): Closure => function (string $attribute, mixed $value, Closure $fail): void {
                        if (($error = SiteRepository::scriptError(is_string($value) ? $value : '')) !== null) {
                            $fail($error);
                        }
                    }),
                Toggle::make('run_composer')
                    ->label(__('zimam.deployments.settings.composer'))
                    ->helperText(__('zimam.deployments.settings.composer_help'))
                    ->visible(fn (): bool => $this->site->runsPhp()),
                TextInput::make('keep_releases')
                    ->label(__('zimam.deployments.settings.keep_releases'))
                    ->helperText(__('zimam.deployments.settings.keep_releases_help'))
                    ->integer()
                    ->required()
                    ->minValue(SiteRepository::MIN_KEEP_RELEASES)
                    ->maxValue(SiteRepository::MAX_KEEP_RELEASES),
            ]);
    }

    private function repository(): ?SiteRepository
    {
        return $this->site->repository;
    }

    /** The site can take a job now: the agent answers, the site is active, and none of its jobs is unfinished. */
    private function canSubmit(): bool
    {
        if (! app(AgentHealth::class)->isReachable()) {
            Notification::make()->title(__('zimam.agent.paused'))->warning()->send();

            return false;
        }
        if ($this->site->status !== Site::ACTIVE || $this->site->hasUnfinishedJob()) {
            Notification::make()->title(__('zimam.sites.busy'))->warning()->send();

            return false;
        }

        return true;
    }

    /**
     * Submits site.repo. A first connect also makes the repository row with
     * its webhook secret and deploy token, shown now and never again; a
     * refusal removes that row. The repository, branch and key are stored by
     * the outcome.
     */
    public function connect(): void
    {
        $state = $this->repositoryForm->getState();
        if (! $this->canSubmit()) {
            return;
        }
        $repository = $this->repository();
        $new = $repository === null;
        $command = new SiteRepo($this->site->name, $state['repository'], $state['branch'], ! $new && (bool) ($state['rotate_key'] ?? false));
        if ($new) {
            $repository = SiteRepository::createFor($this->site, $state['repository'], $state['branch']);
        }
        $undo = function () use ($new, $repository): void {
            if ($new) {
                $repository->delete();
                $this->site->unsetRelation('repository');
            }
        };
        try {
            app(SubmitAgentCommand::class)->run($command, $this->site);
        } catch (Halt) {
            $undo();

            return;
        } catch (AgentValidationFailed $e) {
            $undo();
            foreach ($e->fields() as $field => $message) {
                $this->addError('repositoryData.'.(in_array($field, ['repository', 'branch'], true) ? $field : 'repository'), $message);
            }

            return;
        } catch (AgentException $e) {
            $undo();
            Notification::make()->title($e->getMessage())->danger()->send();

            return;
        }
        if ($new) {
            $this->reveal($repository->webhook_secret, $repository->revealedDeployToken);
            $this->site->setRelation('repository', $repository);
            $this->fillSettings($repository);
        }
        $this->site->update(['last_error' => null]);
        $this->repositoryData['rotate_key'] = false;
        Notification::make()->title(__('zimam.deployments.repository.submitted', ['name' => $this->site->name]))->body(__('zimam.php.submitted_body'))->success()->send();
    }

    /**
     * Stores the deploy settings; they need nothing from the agent until the
     * next deploy. A changed deploy script is stored only through
     * confirmDeployScript, with the password: the script runs as the site's
     * user beside the .env, and its output is in the deployment's log, so
     * whoever may change it can read the secrets the Environment tab asks
     * the password for (R02-D10).
     */
    public function saveSettings(): void
    {
        $repository = $this->repository();
        if ($repository === null) {
            return;
        }
        $state = $this->settingsForm->getState();
        if (self::scriptOf($state) !== (string) $repository->deploy_script) {
            $this->mountAction('confirmDeployScript');

            return;
        }
        $this->storeSettings($repository, $state);
    }

    public function confirmDeployScriptAction(): Action
    {
        return ConfirmsPassword::apply(
            Action::make('confirmDeployScript')
                ->label(__('zimam.deployments.settings.save'))
                ->modalHeading(__('zimam.deployments.settings.confirm_script_heading'))
                ->modalDescription(__('zimam.deployments.settings.confirm_script_description'))
                ->modalSubmitActionLabel(__('zimam.deployments.settings.save'))
                ->visible(fn (): bool => $this->repository() !== null)
                ->action(function (): void {
                    // The form is validated again: the state is the browser's.
                    $this->storeSettings($this->repository(), $this->settingsForm->getState());
                }),
        );
    }

    /** @param array<string, mixed> $state */
    private static function scriptOf(array $state): string
    {
        return (string) ($state['deploy_script'] ?? '');
    }

    /** @param array<string, mixed> $state */
    private function storeSettings(SiteRepository $repository, array $state): void
    {
        $repository->update([
            'auto_deploy' => (bool) ($state['auto_deploy'] ?? false),
            'deploy_script' => self::scriptOf($state),
            'run_composer' => $this->site->runsPhp() && (bool) ($state['run_composer'] ?? false),
            'keep_releases' => (int) $state['keep_releases'],
        ]);
        Notification::make()->title(__('zimam.deployments.settings.saved'))->success()->send();
    }

    /** Puts R02-D8's Laravel preset in the editor, Composer on; saving keeps it. */
    public function applyLaravelPreset(): void
    {
        $this->settingsData = array_merge($this->settingsData ?? [], [
            'deploy_script' => SiteRepository::LARAVEL_PRESET,
            'run_composer' => true,
        ]);
    }

    /** Deploys the branch's head with the saved settings. */
    public function deployNow(): void
    {
        if (! ($this->repository()?->isConnected() ?? false) || ! $this->canSubmit()) {
            return;
        }
        try {
            $deployment = app(StartDeployment::class)->run($this->site, Deployment::MANUAL, by: auth()->user());
        } catch (Halt) {
            return;
        } catch (AgentValidationFailed $e) {
            // The saved settings break a rule of the agent's: say which.
            $fields = [];
            foreach ($e->fields() as $field => $message) {
                $fields[] = "{$field}: {$message}";
            }
            Notification::make()->title(__('zimam.deployments.deploy.refused'))->body(implode("\n", $fields))->danger()->send();

            return;
        } catch (AgentException $e) {
            Notification::make()->title($e->getMessage())->danger()->send();

            return;
        }
        $this->logDeployment = $deployment->id;
        Notification::make()->title(__('zimam.deployments.deploy.submitted', ['name' => $this->site->name]))->body(__('zimam.php.submitted_body'))->success()->send();
    }

    public function showLog(int $deployment): void
    {
        $this->logDeployment = $this->site->deployments()->whereKey($deployment)->value('id');
    }

    public function regenerateWebhookSecretAction(): Action
    {
        return ConfirmsPassword::apply(
            Action::make('regenerateWebhookSecret')
                ->label(__('zimam.deployments.webhook.regenerate'))
                ->color('gray')
                ->modalHeading(__('zimam.deployments.webhook.regenerate_heading'))
                ->modalDescription(__('zimam.deployments.webhook.regenerate_description'))
                ->modalSubmitActionLabel(__('zimam.deployments.webhook.regenerate'))
                ->visible(fn (): bool => $this->repository() !== null)
                ->action(function (): void {
                    $this->reveal(webhookSecret: $this->repository()->regenerateWebhookSecret());
                    Notification::make()->title(__('zimam.deployments.webhook.regenerated'))->success()->send();
                }),
        );
    }

    public function regenerateDeployUrlAction(): Action
    {
        return ConfirmsPassword::apply(
            Action::make('regenerateDeployUrl')
                ->label(__('zimam.deployments.deploy_url.regenerate'))
                ->color('gray')
                ->modalHeading(__('zimam.deployments.deploy_url.regenerate_heading'))
                ->modalDescription(__('zimam.deployments.deploy_url.regenerate_description'))
                ->modalSubmitActionLabel(__('zimam.deployments.deploy_url.regenerate'))
                ->visible(fn (): bool => $this->repository() !== null)
                ->action(function (): void {
                    $this->reveal(deployToken: $this->repository()->regenerateDeployToken());
                    Notification::make()->title(__('zimam.deployments.deploy_url.regenerated'))->success()->send();
                }),
        );
    }

    /** Roll back, offered on each kept release that isn't live. */
    public function rollbackAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('rollback')
                    ->label(__('zimam.deployments.rollback.label'))
                    ->color('warning')
                    ->size('sm')
                    ->modalHeading(fn (array $arguments): string => __('zimam.deployments.rollback.heading', ['release' => $arguments['release'] ?? '']))
                    ->modalDescription(__('zimam.deployments.rollback.description'))
                    ->modalSubmitActionLabel(__('zimam.deployments.rollback.label'))
                    ->visible(fn (array $arguments): bool => in_array($arguments['release'] ?? null, $this->rollbackTargets(), true))
                    ->action(fn (array $arguments) => $this->rollback((string) ($arguments['release'] ?? ''))),
            ),
            fn (): ?string => $this->site->hasUnfinishedJob() ? __('zimam.sites.busy') : null,
        );
    }

    private function rollback(string $release): void
    {
        // Never the kept answer: the agent is asked again, so only a release
        // it keeps now, and that isn't live, can be rolled back to.
        if (! in_array($release, $this->rollbackTargets(fresh: true), true)) {
            Notification::make()->title(__('zimam.deployments.rollback.gone', ['release' => $release]))->warning()->send();

            return;
        }
        if (! $this->canSubmit()) {
            return;
        }
        try {
            app(SubmitAgentCommand::class)->run(new SiteRollback($this->site->name, $release), $this->site);
        } catch (Halt) {
            return;
        } catch (AgentException $e) {
            Notification::make()->title($e->getMessage())->danger()->send();

            return;
        }
        $this->site->update(['last_error' => null]);
        Notification::make()->title(__('zimam.deployments.rollback.submitted', ['name' => $this->site->name, 'release' => $release]))->body(__('zimam.php.submitted_body'))->success()->send();
    }

    /**
     * site.releases's answer, only for a site whose repository is connected.
     * It is read when the tab opens and kept; the next render reads it again
     * once the site's job history has moved (releasesMark), however that
     * happened. $fresh reads it whatever the mark says, for Roll back.
     *
     * @return array<string, mixed>|null
     */
    private function releases(bool $fresh = false): ?array
    {
        if (! ($this->repository()?->isConnected() ?? false)) {
            return null;
        }
        $mark = $this->jobsMark();
        if ($fresh || $mark !== $this->releasesMark) {
            $this->releasesMark = $mark;
            $this->releasesError = null;
            try {
                $this->releasesData = app(AgentClient::class)->call(new SiteReleases($this->site->name))->data;
            } catch (AgentException $e) {
                $this->releasesData = null;
                $this->releasesError = $e->getMessage();
            }
        }

        return $this->releasesData;
    }

    /** The site's newest job and when it finished: what changes when a job starts or ends, and not while it runs. */
    private function jobsMark(): string
    {
        $job = $this->site->jobs()->latest('id')->first(['id', 'finished_at']);

        return $job === null ? 'none' : $job->id.'@'.($job->finished_at?->format('Y-m-d\TH:i:s.u') ?? 'unfinished');
    }

    /** @return list<string> the kept releases that aren't live: the only ones Roll back takes */
    private function rollbackTargets(bool $fresh = false): array
    {
        $ids = [];
        foreach ($this->releases($fresh)['releases'] ?? [] as $release) {
            if (! ($release['active'] ?? false)) {
                $ids[] = (string) $release['id'];
            }
        }

        return $ids;
    }

    /** @return Collection<int, Deployment> */
    private function deployments(): Collection
    {
        return $this->site->deployments()->with(['agentJob', 'creator'])->latest('id')->limit(self::LISTED)->get();
    }

    public function render(): View
    {
        $deployments = $this->deployments();
        $log = $deployments->firstWhere('id', $this->logDeployment) ?? $deployments->first();

        return view('filament.sites.deployments-tab', [
            'repository' => $this->repository(),
            'deployments' => $deployments,
            'logDeploymentRow' => $log,
            'releases' => $this->releases(),
            'releasesError' => $this->releasesError,
            'revealedWebhookSecret' => $this->revealedWebhookSecret,
            'revealedDeployToken' => $this->revealedDeployToken,
            'polling' => $deployments->contains(fn (Deployment $d): bool => ! $d->isFinished()) || $this->site->hasUnfinishedJob(),
        ]);
    }
}
