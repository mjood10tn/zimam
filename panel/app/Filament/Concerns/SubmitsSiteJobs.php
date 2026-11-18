<?php

namespace App\Filament\Concerns;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use Closure;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Support\Exceptions\Halt;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;

/**
 * What the site tabs that submit a whole list as one job share (v0.4.0,
 * R04-D3): the Workers and Schedule tabs. The using component has a
 * `$site` property.
 */
trait SubmitsSiteJobs
{
    /** Disabled while the agent is down or the site can't take a job, with why; $blocker adds a reason. */
    private function guarded(Action $action, ?Closure $blocker = null): Action
    {
        return AgentHealth::guard($action, fn (Action $action): ?string => $this->site->jobBlocker() ?? ($blocker === null ? null : $action->evaluate($blocker)));
    }

    /** Submits a job of the site's; false after saying why it wasn't taken. */
    private function submit(AgentCommand $command, string $refused): bool
    {
        if (! app(AgentHealth::class)->isReachable()) {
            Notification::make()->title(__('zimam.agent.paused'))->warning()->send();

            return false;
        }
        if (($blocker = $this->site->jobBlocker()) !== null) {
            Notification::make()->title($blocker)->warning()->send();

            return false;
        }
        try {
            app(SubmitAgentCommand::class)->run($command, $this->site);
        } catch (Halt) {
            return false;
        } catch (AgentValidationFailed $e) {
            $fields = [];
            foreach ($e->fields() as $field => $message) {
                $fields[] = "{$field}: {$message}";
            }
            Notification::make()->title($refused)->body(implode("\n", $fields))->danger()->send();

            return false;
        } catch (AgentException $e) {
            Notification::make()->title($refused)->body($e->getMessage())->danger()->send();

            return false;
        }

        return true;
    }

    /**
     * Stores the list the agent has just been sent: the site's rows named by
     * $relation become $list, each marked $applying, and a row left out is
     * marked $removing, so it goes only when the job succeeds. Rows keep their
     * ids, so an open row action still finds its model.
     *
     * @param  Collection<int, Model>  $list
     */
    private function storeSubmittedList(string $relation, string $key, Collection $list, string $applying, string $removing): void
    {
        DB::transaction(function () use ($relation, $key, $list, $applying, $removing): void {
            $this->site->{$relation}()->whereNotIn($key, $list->pluck($key))->update(['status' => $removing]);
            foreach ($list as $row) {
                $this->site->{$relation}()->updateOrCreate(
                    [$key => $row->{$key}],
                    [...$row->only($row->getFillable()), 'status' => $applying],
                );
            }
        });
    }
}
