<?php

namespace App\Filament\Pages;

use App\Actions\SubmitAgentCommand;
use App\Actions\SyncValkey;
use App\Agent\AgentHealth;
use App\Agent\Commands\ValkeyInstall;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\ServerStatus;
use App\Models\ServerService;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Text;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Filament\Support\Icons\Heroicon;

/**
 * Valkey: install it, and see how the install went. Each site's own Valkey
 * user is on the site's Cache tab. The row is synced with the agent on every
 * load, and the agent's answer is the truth.
 */
class Cache extends Page
{
    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedBolt;

    protected static ?int $navigationSort = 4;

    public static function getNavigationLabel(): string
    {
        return __('zimam.cache.title');
    }

    public function getTitle(): string
    {
        return __('zimam.cache.title');
    }

    public function mount(): void
    {
        if (app(AgentHealth::class)->isReachable()) {
            app(SyncValkey::class)->run();
        }
    }

    /** Whether Valkey is on the server, as the agent last said. */
    public function valkeyInstalled(): bool
    {
        return ServerStatus::current()?->service(ServerService::VALKEY)['installed'] ?? ServerService::named(ServerService::VALKEY)?->status === ServerService::INSTALLED;
    }

    /** Whether the agent couldn't say if Valkey is installed, and the panel has no row that does. */
    public function valkeyUnknown(): bool
    {
        return ServerStatus::current()?->serviceUnknown(ServerService::VALKEY) === true && ! $this->valkeyInstalled();
    }

    public function content(Schema $schema): Schema
    {
        return $schema->components([
            Text::make(function (): string {
                $service = ServerService::named(ServerService::VALKEY);
                if ($this->valkeyInstalled()) {
                    return __('zimam.cache.installed', ['version' => $service?->version ?? ServerStatus::current()?->service(ServerService::VALKEY)['version'] ?? '']);
                }
                if ($service?->status === ServerService::INSTALLING) {
                    return __('zimam.cache.installing');
                }
                if ($service?->status === ServerService::FAILED) {
                    return __('zimam.cache.failed', ['reason' => (string) $service->last_error]);
                }

                return $this->valkeyUnknown() ? __('zimam.cache.unknown') : __('zimam.cache.not_installed');
            }),
        ]);
    }

    protected function getHeaderActions(): array
    {
        return [
            AgentHealth::guard(
                Action::make('install')
                    ->label(__('zimam.cache.install.label'))
                    ->visible(fn (): bool => ! $this->valkeyInstalled() && ! $this->valkeyUnknown() && ServerService::named(ServerService::VALKEY)?->status !== ServerService::INSTALLING)
                    ->requiresConfirmation()
                    ->modalHeading(__('zimam.cache.install.heading'))
                    ->modalDescription(__('zimam.cache.install.description'))
                    ->modalSubmitActionLabel(__('zimam.cache.install.submit'))
                    ->action(fn () => $this->install()),
            ),
        ];
    }

    /**
     * Submits valkey.install for the service row, marked installing until the
     * outcome. A refusal puts the row back as it was and shows the agent's
     * words; an unreachable agent does the same and halts, after
     * SubmitAgentCommand has told the user.
     */
    private function install(): void
    {
        $row = ServerService::firstOrNew(['name' => ServerService::VALKEY]);
        $previous = $row->exists ? $row->only(['status', 'last_error']) : null;
        $row->fill(['status' => ServerService::INSTALLING, 'last_error' => null])->save();
        $undo = fn () => $previous === null ? $row->delete() : $row->update($previous);
        try {
            app(SubmitAgentCommand::class)->run(new ValkeyInstall, $row);
        } catch (Halt $e) {
            $undo();

            throw $e;
        } catch (AgentException $e) {
            $undo();
            Notification::make()
                ->title($e instanceof AgentValidationFailed && $e->fields() !== [] ? implode(' ', $e->fields()) : $e->getMessage())
                ->danger()
                ->send();

            return;
        }
        Notification::make()->title(__('zimam.cache.install.submitted'))->body(__('zimam.databases.submitted_body'))->success()->send();
    }
}
