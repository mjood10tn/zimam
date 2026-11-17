<?php

namespace App\Filament\Concerns;

use App\Actions\SubmitAgentCommand;
use App\Actions\WriteCredentialsToEnv;
use App\Agent\AgentHealth;
use App\Agent\Commands\ServiceUserCommand;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\AgentJob;
use App\Models\SiteCredential;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Support\Exceptions\Halt;
use Livewire\Attributes\Locked;
use SensitiveParameter;

/**
 * A site tab that sets the password of the site's service user (MySQL or
 * Valkey), shows it once and writes the connection to .env.
 *
 * The password is made in the rotate request, sent to the agent once and
 * never kept. It is only in that response's HTML, inside a wire:ignore
 * block the browser keeps until the block is closed, and hidden there until
 * the job has succeeded. Writing it to .env sends the browser's copy as
 * writeEnv()'s argument, so it is only in that request. No snapshot,
 * table, log, notification, session or queue payload ever carries it.
 *
 * The using component is a Livewire component with a locked `site`, and
 * names its service: the credential kind, the language group holding the
 * rotate.* and write_env.* strings, the command and the .env keys.
 */
trait RotatesServicePassword
{
    /** The agent_jobs row of the password being set; null when no password is on screen. */
    #[Locked]
    public ?int $rotationJobId = null;

    /** Changes with each rotation, so the browser swaps in the newly made password block. */
    #[Locked]
    public ?string $rotationKey = null;

    /** The password is active: the job succeeded. */
    #[Locked]
    public bool $rotationDone = false;

    /** Whether the user existed before this rotation, for what a failure says. */
    #[Locked]
    public bool $hadUser = false;

    /** Why the last rotation failed, in words; the password is gone with it. */
    #[Locked]
    public ?string $rotationFailure = null;

    /** When the password was confirmed (a Unix time): writing .env needs it as fresh as a confirmation lasts. */
    #[Locked]
    public ?int $confirmedAt = null;

    /**
     * A keyed hash of the password made here, so writeEnv() takes only that
     * password back from the browser, not any string of the right shape. The
     * key is the app key, so the hash of a 256-bit random value reveals nothing.
     */
    #[Locked]
    public ?string $passwordProof = null;

    /** The .env was written with this password. */
    #[Locked]
    public bool $envWritten = false;

    /** The new password, only in the response of the request that made it. */
    private ?string $password = null;

    /** The kind of site_credentials row this service's user has. */
    abstract protected function credentialKind(): string;

    /** The language group (zimam.<group>.rotate.*, zimam.<group>.write_env.*). */
    abstract protected function lang(): string;

    abstract protected function serviceInstalled(): bool;

    abstract protected function passwordCommand(#[SensitiveParameter] string $password): ServiceUserCommand;

    /**
     * The .env keys for $password, or null when something the user chose is
     * wrong (the hook has said what).
     *
     * @return ?array<string, string>
     */
    abstract protected function envValues(#[SensitiveParameter] string $password): ?array;

    public function rotateAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('rotate')
                    ->label(fn (): string => $this->credential() === null ? __("zimam.{$this->lang()}.rotate.label_first") : __("zimam.{$this->lang()}.rotate.label"))
                    ->modalHeading(fn (): string => __("zimam.{$this->lang()}.rotate.heading", ['user' => SiteCredential::usernameFor($this->site->name)]))
                    ->modalDescription(__("zimam.{$this->lang()}.rotate.description"))
                    ->modalSubmitActionLabel(__("zimam.{$this->lang()}.rotate.submit"))
                    ->action(fn () => $this->rotate()),
            ),
            fn (): ?string => match (true) {
                ! $this->serviceInstalled() => __("zimam.{$this->lang()}.rotate.not_installed"),
                ($blocker = $this->site->jobBlocker()) !== null => $blocker,
                default => null,
            },
        );
    }

    private function credential(): ?SiteCredential
    {
        return $this->site->credentials()->where('kind', $this->credentialKind())->first();
    }

    /**
     * Makes the password, sends it and keeps it for this response's HTML
     * only. A busy site is refused instead of queued, since a queued command
     * would hold the password.
     */
    private function rotate(): void
    {
        if (($blocker = $this->site->jobBlocker()) !== null) {
            Notification::make()->title($blocker)->warning()->send();

            return;
        }
        $user = SiteCredential::usernameFor($this->site->name);
        $password = ServiceUserCommand::generatePassword();
        $hadUser = $this->credential() !== null;
        try {
            $job = app(SubmitAgentCommand::class)->run($this->passwordCommand($password), $this->site, waitWhenBusy: false);
        } catch (Halt) {
            return;
        } catch (AgentBusy) {
            Notification::make()->title(__("zimam.{$this->lang()}.rotate.busy"))->warning()->send();

            return;
        } catch (AgentException $e) {
            Notification::make()
                ->title(__("zimam.{$this->lang()}.rotate.refused"))
                ->body($e instanceof AgentValidationFailed && $e->fields() !== [] ? implode(' ', $e->fields()) : $e->getMessage())
                ->danger()
                ->send();

            return;
        }
        $this->rotationJobId = $job->id;
        $this->rotationKey = bin2hex(random_bytes(8));
        $this->rotationDone = false;
        $this->rotationFailure = null;
        $this->hadUser = $hadUser;
        $this->confirmedAt = now()->getTimestamp();
        $this->envWritten = false;
        $this->passwordProof = self::proof($password);
        $this->password = $password;
        Notification::make()->title(__("zimam.{$this->lang()}.rotate.submitted", ['user' => $user]))->success()->send();
    }

    /** Polled while the password is being set: follows its job, and drops the password block if the job failed. */
    public function refreshRotation(): void
    {
        $job = $this->rotationJobId === null ? null : AgentJob::find($this->rotationJobId);
        if ($job === null || ! $job->isFinished()) {
            return;
        }
        if ($job->status === AgentJob::SUCCEEDED) {
            $this->rotationDone = true;

            return;
        }
        $reason = (string) $job->error_message;
        $this->rotationFailure = __($this->hadUser ? "zimam.{$this->lang()}.rotate.failed_kept" : "zimam.{$this->lang()}.rotate.failed_new", ['reason' => $reason]);
        $this->closeRotation();
    }

    /** The password is shown once: closing it removes it from the page for good. */
    public function closeRotation(): void
    {
        $this->rotationJobId = null;
        $this->rotationKey = null;
        $this->rotationDone = false;
        $this->confirmedAt = null;
        $this->envWritten = false;
        $this->passwordProof = null;
        $this->password = null;
    }

    /**
     * Sets the connection in the site's .env, with the password the browser
     * still holds. Only after the job succeeded and while the confirmation
     * of the password still holds; the browser's text is the only copy.
     */
    public function writeEnv(#[SensitiveParameter] mixed $password): void
    {
        $this->refreshRotation();
        if ($this->rotationJobId === null || ! $this->rotationDone) {
            return;
        }
        if ($this->confirmedAt === null || now()->getTimestamp() - $this->confirmedAt > (int) config('auth.password_timeout')) {
            $this->closeRotation();
            Notification::make()->title(__("zimam.{$this->lang()}.write_env.expired"))->warning()->send();

            return;
        }
        if (! is_string($password) || ! preg_match(ServiceUserCommand::PASSWORD_PATTERN, $password)) {
            return;
        }
        if ($this->passwordProof === null || ! hash_equals($this->passwordProof, self::proof($password))) {
            Notification::make()->title(__("zimam.{$this->lang()}.write_env.mismatch"))->warning()->send();

            return;
        }
        if (($values = $this->envValues($password)) === null) {
            return;
        }
        if (! app(AgentHealth::class)->isReachable()) {
            Notification::make()->title(__('zimam.agent.paused'))->warning()->send();

            return;
        }
        try {
            app(WriteCredentialsToEnv::class)->run($this->site, $values);
        } catch (Halt) {
            return;
        } catch (AgentBusy) {
            Notification::make()->title(__("zimam.{$this->lang()}.rotate.busy"))->warning()->send();

            return;
        } catch (AgentUnavailable) {
            Notification::make()->title(__('zimam.agent.paused'))->warning()->send();

            return;
        } catch (AgentException $e) {
            Notification::make()->title(__("zimam.{$this->lang()}.write_env.refused"))->body($e->getMessage())->danger()->send();

            return;
        }
        $this->envWritten = true;
        Notification::make()->title(__("zimam.{$this->lang()}.write_env.submitted", ['name' => $this->site->name]))->body(__('zimam.databases.submitted_body'))->success()->send();
    }

    private static function proof(#[SensitiveParameter] string $password): string
    {
        return hash_hmac('sha256', $password, (string) config('app.key'));
    }

    /**
     * What the rotation block of the view needs.
     *
     * @return array<string, mixed>
     */
    protected function rotationViewData(): array
    {
        $job = $this->rotationJobId === null ? null : AgentJob::find($this->rotationJobId);

        return [
            'password' => $this->password,
            'credential' => $this->credential(),
            'installed' => $this->serviceInstalled(),
            'pending' => $job !== null && ! $job->isFinished(),
            'lang' => $this->lang(),
        ];
    }
}
