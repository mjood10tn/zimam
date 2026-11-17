<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Actions\SyncMysql;
use App\Actions\WriteCredentialsToEnv;
use App\Agent\AgentHealth;
use App\Agent\Commands\DbUser;
use App\Agent\Commands\ServiceUserCommand;
use App\Filament\Concerns\RotatesServicePassword;
use App\Models\ServerService;
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
 * A site's Database tab: its databases, its MySQL user, a new password and
 * writing the connection to .env. The password is handled as
 * RotatesServicePassword describes.
 */
class DatabaseTab extends Component implements HasActions, HasSchemas
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use RotatesServicePassword;

    #[Locked]
    public Site $site;

    /** The database for DB_DATABASE; checked against the site's own in envValues(). */
    public ?string $envDatabase = null;

    public function mount(): void
    {
        if (app(AgentHealth::class)->isReachable()) {
            app(SyncMysql::class)->runForSite($this->site);
        }
        $this->envDatabase = $this->site->databases()->orderBy('name')->value('name');
    }

    public function mysqlInstalled(): bool
    {
        return ServerService::named(ServerService::MYSQL)?->status === ServerService::INSTALLED;
    }

    protected function credentialKind(): string
    {
        return SiteCredential::MYSQL;
    }

    protected function lang(): string
    {
        return 'databases';
    }

    protected function serviceInstalled(): bool
    {
        return $this->mysqlInstalled();
    }

    protected function passwordCommand(#[SensitiveParameter] string $password): ServiceUserCommand
    {
        return new DbUser($this->site->name, $password);
    }

    protected function envValues(#[SensitiveParameter] string $password): ?array
    {
        $this->resetErrorBag('envDatabase');
        $databases = $this->site->databases()->orderBy('name')->pluck('name')->all();
        if ($databases === []) {
            $this->addError('envDatabase', __('zimam.databases.write_env.no_database'));

            return null;
        }
        if (! in_array($this->envDatabase, $databases, true)) {
            $this->addError('envDatabase', __('zimam.databases.write_env.invalid_database'));

            return null;
        }

        return WriteCredentialsToEnv::mysqlValues($this->site->name, $this->envDatabase, $password);
    }

    public function render(): View
    {
        return view('filament.sites.database-tab', [
            ...$this->rotationViewData(),
            'databases' => $this->site->databases()->orderBy('name')->get(),
        ]);
    }
}
