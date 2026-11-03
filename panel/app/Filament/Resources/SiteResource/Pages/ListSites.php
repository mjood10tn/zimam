<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Agent\AgentHealth;
use App\Filament\Resources\SiteResource;
use Filament\Actions\CreateAction;
use Filament\Resources\Pages\ListRecords;

class ListSites extends ListRecords
{
    protected static string $resource = SiteResource::class;

    protected function getHeaderActions(): array
    {
        return [AgentHealth::guard(CreateAction::make())];
    }
}
