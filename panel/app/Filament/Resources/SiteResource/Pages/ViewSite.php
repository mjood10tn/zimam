<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\SitePhp;
use App\Agent\Exceptions\AgentException;
use App\Agent\ServerStatus;
use App\Filament\Resources\SiteResource;
use App\Models\Site;
use Filament\Actions\Action;
use Filament\Forms\Components\Select;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\ViewRecord;
use Filament\Support\Exceptions\Halt;

/** A site's page: Overview, Jobs, Security and Logs tabs; Switch PHP version for an active PHP site (§9.5); Retry and Delete for a failed site (§9.2). */
class ViewSite extends ViewRecord
{
    protected static string $resource = SiteResource::class;

    protected function getHeaderActions(): array
    {
        return [
            AgentHealth::guard(
                Action::make('switchPhp')
                    ->label(__('zimam.sites.switch.label'))
                    ->modalHeading(fn (Site $record): string => __('zimam.sites.switch.heading', ['name' => $record->name]))
                    ->modalDescription(__('zimam.sites.switch.description'))
                    ->modalSubmitActionLabel(__('zimam.sites.switch.submit'))
                    ->visible(fn (Site $record): bool => $record->type === Site::PHP && $record->status === Site::ACTIVE)
                    ->schema([
                        Select::make('php_version')
                            ->label(__('zimam.sites.fields.php_version'))
                            ->options(fn (): array => $this->switchPhpOptions())
                            ->noOptionsMessage(__('zimam.sites.switch.none'))
                            ->required()
                            ->in(fn (): array => array_keys($this->switchPhpOptions())),
                    ])
                    ->action(fn (Site $record, array $data) => $this->switchPhp($record, $data['php_version'])),
                fn (Site $record): ?string => $record->hasUnfinishedJob() ? __('zimam.sites.switch.busy') : null,
            ),
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

    /**
     * The versions the site can move to: installed, supported, and not the
     * one it runs.
     *
     * @return array<string, string> version => label
     */
    public function switchPhpOptions(): array
    {
        $options = ServerStatus::current()?->sitePhpOptions() ?? [];
        unset($options[$this->getRecord()->php_version]);

        return $options;
    }

    /**
     * Submits site.php. The site stays active on its version until the
     * outcome moves it; a refusal is shown as the agent worded it.
     */
    private function switchPhp(Site $site, string $version): void
    {
        try {
            app(SubmitAgentCommand::class)->run(new SitePhp($site->name, $version), $site);
        } catch (AgentException $e) {
            Notification::make()->title($e->getMessage())->danger()->send();

            return;
        }
        $site->update(['last_error' => null]);
        Notification::make()->title(__('zimam.sites.switch.submitted', ['name' => $site->name, 'version' => $version]))->success()->send();
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
