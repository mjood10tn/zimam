<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\WriteCredentialsToEnv;
use App\Agent\Commands\CacheUser;
use App\Agent\Commands\ServiceUserCommand;
use App\Agent\ServerStatus;
use App\Filament\Concerns\RotatesServicePassword;
use App\Models\Site;
use App\Models\SiteCredential;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Illuminate\Contracts\View\View;
use Livewire\Attributes\Locked;
use Livewire\Component;
use SensitiveParameter;

/**
 * A site's Cache tab: its Valkey user, a new password, writing the
 * connection to .env and the limits of sharing one Valkey. The password is
 * handled as RotatesServicePassword describes. Before Valkey is installed
 * the tab only says so and links to where to install it.
 */
class CacheTab extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use RotatesServicePassword;

    #[Locked]
    public Site $site;

    /** Whether the agent last said Valkey is installed (system.status, cached for a few seconds). */
    public function valkeyInstalled(): bool
    {
        return (bool) (ServerStatus::current()?->data['services']['valkey']['installed'] ?? false);
    }

    protected function credentialKind(): string
    {
        return SiteCredential::VALKEY;
    }

    protected function lang(): string
    {
        return 'cache';
    }

    protected function serviceInstalled(): bool
    {
        return $this->valkeyInstalled();
    }

    protected function passwordCommand(#[SensitiveParameter] string $password): ServiceUserCommand
    {
        return new CacheUser($this->site->name, $password);
    }

    protected function envValues(#[SensitiveParameter] string $password): ?array
    {
        return WriteCredentialsToEnv::valkeyValues($this->site->name, $password);
    }

    public function render(): View
    {
        return view('filament.sites.cache-tab', $this->rotationViewData());
    }
}
