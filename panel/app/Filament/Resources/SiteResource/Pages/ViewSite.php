<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Exceptions\AgentException;
use App\Filament\Resources\SiteResource;
use App\Models\Site;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\ViewRecord;
use Filament\Support\Exceptions\Halt;

/** A site's page: Overview, Jobs, Security and Logs tabs; Retry and Delete for a failed site (§9.2). */
class ViewSite extends ViewRecord
{
    protected static string $resource = SiteResource::class;

    protected function getHeaderActions(): array
    {
        return [
            AgentHealth::guard(
                Action::make('retry')
                    ->label(__('zimam.sites.retry.label'))
                    ->visible(fn (Site $record): bool => $record->status === Site::FAILED)
                    ->action(fn (Site $record) => $this->retry($record)),
            ),
            // site.destroy arrives with Task 29.
            Action::make('delete')
                ->label(__('zimam.sites.delete.label'))
                ->color('danger')
                ->visible(fn (Site $record): bool => $record->status === Site::FAILED)
                ->disabled()
                ->tooltip(__('zimam.sites.delete.later')),
        ];
    }

    /** Submits site.provision again; the agent continues where the last attempt stopped. */
    private function retry(Site $site): void
    {
        $lastError = $site->last_error;
        $site->update(['status' => Site::PROVISIONING, 'last_error' => null]);
        try {
            app(SubmitAgentCommand::class)->run($site->provisionCommand(), $site);
        } catch (Halt $e) {
            $site->update(['status' => Site::FAILED, 'last_error' => $lastError]);

            throw $e;
        } catch (AgentException $e) {
            $site->update(['status' => Site::FAILED, 'last_error' => $e->getMessage()]);
            Notification::make()->title($e->getMessage())->danger()->send();

            return;
        }
        Notification::make()->title(__('zimam.sites.retry.submitted', ['name' => $site->name]))->success()->send();
    }
}
