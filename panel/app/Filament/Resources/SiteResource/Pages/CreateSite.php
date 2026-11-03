<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\ServerStatus;
use App\Filament\Resources\SiteResource;
use App\Models\Site;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Resources\Pages\CreateRecord;
use Filament\Support\Exceptions\Halt;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

/**
 * Creates the site's rows, then submits site.provision. A refusal removes
 * the rows again: the agent's field errors land on the form, any other
 * reason in a notification.
 */
class CreateSite extends CreateRecord
{
    protected static string $resource = SiteResource::class;

    protected static bool $canCreateAnother = false;

    /**
     * The versions a new site can use: installed and still supported (§8.1),
     * straight from system.status, since php_versions may not be synced yet.
     *
     * @return array<string, string> version => label
     */
    public function phpVersionOptions(): array
    {
        return ServerStatus::current()?->sitePhpOptions() ?? [];
    }

    public function defaultPhpVersion(): ?string
    {
        $default = ServerStatus::current()?->data['php']['default'] ?? null;

        return array_key_exists((string) $default, $this->phpVersionOptions()) ? $default : null;
    }

    protected function getCreateFormAction(): Action
    {
        return AgentHealth::guard(parent::getCreateFormAction());
    }

    /** @param array<string, mixed> $data */
    protected function handleRecordCreation(array $data): Model
    {
        $site = DB::transaction(function () use ($data): Site {
            $site = Site::create([
                'name' => $data['name'],
                'type' => $data['type'],
                'php_version' => $data['type'] === Site::PHP ? $data['php_version'] : null,
                'status' => Site::PROVISIONING,
            ]);
            $site->domains()->create(['hostname' => $data['domain'], 'is_primary' => true]);
            $site->securitySettings()->create();

            return $site;
        });

        try {
            app(SubmitAgentCommand::class)->run($site->provisionCommand(), $site);
        } catch (AgentValidationFailed $e) {
            $site->delete();

            throw ValidationException::withMessages(collect($e->fields())->mapWithKeys(fn (string $message, string $field): array => ["data.{$field}" => $message])->all());
        } catch (AgentException $e) {
            $site->delete();
            Notification::make()->title($e->getMessage())->danger()->send();

            throw new Halt;
        } catch (Halt $e) {
            $site->delete();

            throw $e;
        }

        return $site;
    }

    protected function getCreatedNotificationTitle(): ?string
    {
        return __('zimam.sites.create.submitted', ['name' => $this->record->name]);
    }

    protected function getRedirectUrl(): string
    {
        return SiteResource::getUrl('view', ['record' => $this->record]);
    }
}
