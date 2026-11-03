<?php

namespace App\Filament\Resources\AgentJobResource\Pages;

use App\Agent\AgentClient;
use App\Agent\AgentHealth;
use App\Agent\Commands\JobCancel;
use App\Agent\Exceptions\AgentException;
use App\Filament\Resources\AgentJobResource;
use App\Models\AgentJob;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\ViewRecord;

/** A job's page: status, times, error and the live log; Cancel while it's queued or running (§7.3). */
class ViewAgentJob extends ViewRecord
{
    protected static string $resource = AgentJobResource::class;

    protected function getHeaderActions(): array
    {
        return [
            AgentHealth::guard(
                Action::make('cancel')
                    ->label(__('zimam.jobs.cancel.label'))
                    ->color('danger')
                    ->requiresConfirmation()
                    ->modalHeading(__('zimam.jobs.cancel.heading'))
                    ->modalDescription(__('zimam.jobs.cancel.description'))
                    ->visible(fn (AgentJob $record): bool => $record->agent_job_id !== null
                        && in_array($record->status, [AgentJob::QUEUED, AgentJob::RUNNING], true))
                    ->action(function (AgentJob $record): void {
                        try {
                            app(AgentClient::class)->call(new JobCancel($record->agent_job_id));
                        } catch (AgentException $e) {
                            Notification::make()->title($e->getMessage())->danger()->send();

                            return;
                        }
                        // TrackAgentJob records the outcome on its next poll.
                        Notification::make()->title(__('zimam.jobs.cancel.done'))->success()->send();
                    }),
            ),
        ];
    }
}
