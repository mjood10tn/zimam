<?php

namespace App\Livewire;

use App\Filament\Resources\AgentJobResource;
use App\Models\Site;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

/** A site's Jobs tab: the jobs whose outcome updates the site, as the Jobs screen lists them. */
class SiteJobs extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public Site $site;

    public function table(Table $table): Table
    {
        return AgentJobResource::table($table)
            ->query($this->site->jobs()->getQuery())
            ->filters([]);
    }

    public function render(): string
    {
        return <<<'BLADE'
            <div>
                {{ $this->table }}
            </div>
            BLADE;
    }
}
