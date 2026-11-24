<?php

namespace App\Livewire;

use App\Agent\AgentClient;
use App\Agent\Commands\ChangeConfirm;
use App\Agent\Commands\ChangeRevert;
use App\Agent\Commands\FirewallStatus;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentNotFound;
use App\Filament\Pages\Backups\DestinationsTable;
use App\Models\FirewallRule;
use App\Models\GuardedChange;
use App\Settings\PanelSettings;
use App\Support\DisplayTime;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Notifications\Notification;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Exceptions\Halt;
use Illuminate\Contracts\View\View;
use Livewire\Attributes\On;
use Livewire\Component;

/**
 * The guarded-change countdown (R06-D1, D2): while a firewall or SSH change
 * waits, a modal counts down to its deadline and only Keep confirms it. It
 * opens again on every page load until the change is kept or reverted, and a
 * change that ran out while nobody was looking (the owner's address changed,
 * say) is reported as reverted on the next load.
 */
class GuardedChangeModal extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    /** A firewall or SSH job was just sent: poll until its change shows up. */
    public bool $watching = false;

    public function mount(): void
    {
        $this->settle();
    }

    #[On('guarded-change-started')]
    public function watch(): void
    {
        $this->watching = true;
    }

    /** Reports changes whose deadline passed, and opens the modal for the one that waits. */
    public function settle(): void
    {
        foreach (GuardedChange::settleExpired() as $change) {
            Notification::make()
                ->title(__('zimam.server_security.change.expired', [
                    'kind' => __("zimam.server_security.change.kinds.{$change->kind}"),
                    'time' => DisplayTime::format($change->created_at, 'H:i'),
                ]))
                ->warning()
                ->persistent()
                ->send();
            $this->ended();
        }
        $current = GuardedChange::current();
        if ($current !== null) {
            $this->watching = false;
            if ($this->mountedActions === []) {
                $this->mountAction('review');
            }
        } elseif ($this->mountedActions !== []) {
            $this->unmountAction();
        }
    }

    public function polls(): bool
    {
        return $this->watching || GuardedChange::current() !== null || GuardedChange::starting();
    }

    public function reviewAction(): Action
    {
        $change = fn (): ?GuardedChange => GuardedChange::current();

        return Action::make('review')
            ->modalHeading(fn (): string => __('zimam.server_security.change.heading.'.($change()?->kind ?? GuardedChange::FIREWALL)))
            ->modalDescription(fn (): string => __('zimam.server_security.change.body.'.($change()?->kind ?? GuardedChange::FIREWALL)))
            ->modalContent(fn (): View => view('filament.server-security.countdown', ['deadline' => $change()?->deadline]))
            ->modalSubmitActionLabel(__('zimam.server_security.change.keep'))
            // Leaving the modal decides nothing; the timer would. Keep or Revert now.
            ->modalCancelAction(false)
            ->modalCloseButton(false)
            ->closeModalByClickingAway(false)
            ->closeModalByEscaping(false)
            ->extraModalFooterActions(fn (Action $action): array => [
                $action->makeModalSubmitAction('revert', arguments: ['revert' => true])
                    ->label(__('zimam.server_security.change.revert'))
                    ->color('gray'),
            ])
            ->action(fn (array $arguments) => $this->end(revert: (bool) ($arguments['revert'] ?? false)));
    }

    /**
     * Keeps or reverts the waiting change. One the agent no longer has was
     * reverted by its timer. An unreachable agent keeps the modal open, so
     * the owner can try again before the deadline.
     */
    private function end(bool $revert): void
    {
        $change = GuardedChange::query()->where('status', GuardedChange::PENDING)->latest('id')->first();
        if ($change === null) {
            return;
        }
        try {
            app(AgentClient::class)->call($revert ? new ChangeRevert($change->agent_change_id) : new ChangeConfirm($change->agent_change_id));
        } catch (AgentNotFound) {
            $change->update(['status' => GuardedChange::REVERTED]);
            Notification::make()->title(__('zimam.server_security.change.gone'))->warning()->send();
            $this->ended();

            return;
        } catch (AgentException $e) {
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            throw new Halt;
        }
        $change->update(['status' => $revert ? GuardedChange::REVERTED : GuardedChange::CONFIRMED]);
        if (! $revert && $change->kind === GuardedChange::SSH) {
            app(PanelSettings::class)->setSsh($change->params);
        }
        Notification::make()->title(__($revert ? 'zimam.server_security.change.reverted' : 'zimam.server_security.change.kept'))->success()->send();
        $this->ended();
    }

    /** The firewall's rows follow the server again (an SSH port change moves its rules too). */
    private function ended(): void
    {
        try {
            FirewallRule::syncFrom(app(AgentClient::class)->call(new FirewallStatus)->data);
        } catch (AgentException) {
            // The page's next load syncs them.
        }
        $this->dispatch('guarded-change-ended');
    }

    public function render(): View
    {
        return view('livewire.guarded-change-modal');
    }
}
