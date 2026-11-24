<?php

namespace App\Filament\Pages\ServerSecurity;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentHealth;
use App\Agent\Commands\CloudflareMode;
use App\Agent\Exceptions\AgentException;
use App\Filament\Actions\ConfirmsPassword;
use App\Filament\Pages\Backups\DestinationsTable;
use App\Models\Domain;
use App\Models\GuardedChange;
use App\Models\SecurityFinding;
use App\Settings\PanelSettings;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Schemas\Schema;
use Filament\Support\Exceptions\Halt;
use Illuminate\Contracts\View\View;
use Livewire\Attributes\On;
use Livewire\Component;

/**
 * The Server security page's Cloudflare mode (R06-D7, D9). Turning the
 * origin restriction on lists every site, and this panel, that stops
 * answering unless it is behind Cloudflare (OQ4); it is a guarded change, so
 * a panel cut off by it reverts by itself.
 *
 * @property-read Schema $form
 */
class CloudflareForm extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;

    /** @var array<string, mixed> */
    public ?array $data = [];

    #[On('guarded-change-ended')]
    public function mount(): void
    {
        $this->form->fill(app(PanelSettings::class)->cloudflare());
    }

    public function form(Schema $schema): Schema
    {
        return $schema->statePath('data')->components([
            Toggle::make('enabled')
                ->label(__('zimam.server_security.cloudflare.fields.enabled'))
                ->live(),
            Toggle::make('restrict_origin')
                ->label(__('zimam.server_security.cloudflare.fields.restrict_origin'))
                ->helperText(__('zimam.server_security.cloudflare.fields.restrict_origin_help'))
                ->disabled(fn (Get $get): bool => ! $get('enabled')),
        ]);
    }

    /** What the latest check said of the ranges, in Cloudflare mode. */
    public function ranges(): ?string
    {
        return SecurityFinding::query()->where('scope', 'server')->where('check', 'cloudflare_ranges_fresh')->value('detail');
    }

    /** Whether the form turns the origin restriction on: then sites and the panel may stop answering. */
    private function restricting(): bool
    {
        return ($this->data['enabled'] ?? false) && ($this->data['restrict_origin'] ?? false)
            && ! app(PanelSettings::class)->cloudflare()['restrict_origin'];
    }

    private function restrictWarning(): string
    {
        $hosts = Domain::query()->orderBy('hostname')->pluck('hostname')->all();
        $panel = parse_url((string) config('app.url'), PHP_URL_HOST);
        if (is_string($panel) && $panel !== '') {
            $hosts[] = __('zimam.server_security.cloudflare.restrict_panel', ['host' => $panel]);
        }

        return __('zimam.server_security.cloudflare.restrict_warning').' '.implode(', ', $hosts).'. '.__('zimam.server_security.cloudflare.restrict_guard');
    }

    public function saveAction(): Action
    {
        return AgentHealth::guard(
            ConfirmsPassword::apply(
                Action::make('save')
                    ->label(__('zimam.server_security.cloudflare.save.label'))
                    ->modalHeading(__('zimam.server_security.cloudflare.save.heading'))
                    ->modalDescription(fn (): string => $this->restricting() ? $this->restrictWarning() : __('zimam.server_security.cloudflare.save.description'))
                    ->modalSubmitActionLabel(__('zimam.server_security.cloudflare.save.label'))
                    ->action(function (): void {
                        $data = $this->form->getState();
                        $enabled = (bool) ($data['enabled'] ?? false);
                        $this->submit(new CloudflareMode($enabled, $enabled && (bool) ($data['restrict_origin'] ?? false)));
                    }),
            ),
            fn (): ?string => GuardedChange::blocker(),
        );
    }

    private function submit(CloudflareMode $command): void
    {
        try {
            app(SubmitAgentCommand::class)->run($command);
        } catch (Halt $e) {
            throw $e;
        } catch (AgentException $e) {
            Notification::make()->title(DestinationsTable::reason($e))->danger()->send();

            return;
        }
        Notification::make()->title(__('zimam.server_security.cloudflare.submitted'))->success()->send();
        $this->dispatch('guarded-change-started');
    }

    public function render(): View
    {
        return view('filament.server-security.cloudflare');
    }
}
