<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\AgentHealth;
use App\Agent\Commands\SiteEnvRead;
use App\Agent\Commands\SiteEnvWrite;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Filament\Actions\ConfirmsPassword;
use App\Models\Site;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Exceptions\Halt;
use Illuminate\Contracts\View\View;
use Livewire\Attributes\Locked;
use Livewire\Component;
use SensitiveParameter;

/**
 * A site's Environment tab (R02-D10): its ~/site/shared/.env, read through
 * site.env.read only after the password is confirmed, and saved through
 * site.env.write.
 *
 * The content is never component state. The reveal request holds it in a
 * private property, so it is only in that response's HTML, inside a
 * wire:ignore block the browser keeps (edits included) until the editor
 * closes; Save sends the browser's text as save()'s argument, so it is only
 * in that request. No snapshot, table, log or notification ever carries it,
 * and the tab never polls.
 */
class EnvironmentTab extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    #[Locked]
    public Site $site;

    /** Whether the editor is open: only the password-confirmed reveal opens it. */
    #[Locked]
    public bool $revealed = false;

    /** The site has no .env yet: the editor opens empty, and saving creates it. */
    #[Locked]
    public bool $missing = false;

    /** Changes with each reveal, so the browser swaps in the newly read editor. */
    #[Locked]
    public ?string $editorKey = null;

    /** Why the .env couldn't be read: the agent's message, which never quotes the file. */
    #[Locked]
    public ?string $readError = null;

    /**
     * When the password was confirmed (a Unix time). The reveal lasts as long
     * as a password confirmation does (auth.password_timeout); after that,
     * save() closes the editor and asks for the password again.
     */
    #[Locked]
    public ?int $revealedAt = null;

    /** The .env as read, only in the response of the reveal request. */
    private ?string $content = null;

    /** The agent's checkTextFile, in bytes as the agent counts them: null when $content can be saved. */
    public static function contentError(#[SensitiveParameter] string $content): ?string
    {
        return match (true) {
            strlen($content) > SiteEnvWrite::MAX_BYTES => __('zimam.environment.validation.size', ['max' => SiteEnvWrite::MAX_BYTES]),
            ! mb_check_encoding($content, 'UTF-8') => __('zimam.environment.validation.utf8'),
            str_contains($content, "\0") => __('zimam.environment.validation.nul'),
            default => null,
        };
    }

    public function revealAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('reveal')
                    ->label(__('zimam.environment.reveal'))
                    ->color('gray')
                    ->modalHeading(__('zimam.environment.reveal_heading'))
                    ->modalDescription(__('zimam.environment.reveal_description'))
                    ->modalSubmitActionLabel(__('zimam.environment.reveal'))
                    ->action(fn () => $this->reveal()),
            ),
        );
    }

    /** Reads the .env; a missing one opens an empty editor, any other error none. */
    private function reveal(): void
    {
        $this->hide();
        try {
            $content = app(AgentClient::class)->call(new SiteEnvRead($this->site->name))->data['content'] ?? null;
        } catch (AgentNotFound) {
            $content = '';
            $this->missing = true;
        } catch (AgentException $e) {
            $this->readError = $e->getMessage();

            return;
        }
        $this->content = is_string($content) ? $content : '';
        $this->revealed = true;
        $this->revealedAt = now()->getTimestamp();
        $this->editorKey = bin2hex(random_bytes(8));
    }

    /**
     * Submits the browser's text as site.env.write. A refusal leaves the
     * editor open, so the browser keeps what was typed; success closes it.
     */
    public function save(#[SensitiveParameter] mixed $content): void
    {
        if (! $this->revealed) {
            return;
        }
        if ($this->revealedAt === null || now()->getTimestamp() - $this->revealedAt > (int) config('auth.password_timeout')) {
            $this->hide();
            Notification::make()->title(__('zimam.environment.expired'))->warning()->send();

            return;
        }
        $this->resetErrorBag('content');
        if (! is_string($content)) {
            $this->addError('content', __('zimam.environment.validation.utf8'));

            return;
        }
        if (($error = self::contentError($content)) !== null) {
            $this->addError('content', $error);

            return;
        }
        if (! $this->canSubmit()) {
            return;
        }
        try {
            app(SubmitAgentCommand::class)->run(new SiteEnvWrite($this->site->name, $content), $this->site);
        } catch (Halt) {
            return;
        } catch (AgentValidationFailed $e) {
            foreach ($e->fields() as $field => $message) {
                $this->addError('content', $field === 'content' ? $message : __('zimam.environment.field_error', ['field' => $field, 'message' => $message]));
            }

            return;
        } catch (AgentException $e) {
            Notification::make()->title(__('zimam.environment.refused'))->body($e->getMessage())->danger()->send();

            return;
        }
        $this->hide();
        $this->site->update(['last_error' => null]);
        Notification::make()->title(__('zimam.environment.submitted', ['name' => $this->site->name]))->body(__('zimam.php.submitted_body'))->success()->send();
    }

    public function cancel(): void
    {
        $this->hide();
    }

    private function hide(): void
    {
        $this->content = null;
        $this->revealed = false;
        $this->revealedAt = null;
        $this->missing = false;
        $this->editorKey = null;
        $this->readError = null;
        $this->resetErrorBag();
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

    public function render(): View
    {
        return view('filament.sites.environment-tab', ['content' => $this->content]);
    }
}
