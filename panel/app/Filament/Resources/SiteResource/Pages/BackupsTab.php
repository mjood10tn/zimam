<?php

namespace App\Filament\Resources\SiteResource\Pages;

use App\Models\AgentJob;
use App\Models\Site;
use Illuminate\Contracts\View\View;
use Livewire\Attributes\Locked;
use Livewire\Component;

/**
 * A site's Backups tab: the plans that include the site, their runs (with
 * restore, from RunsTable), and the site's recent restores with what each
 * wrote and the links a files restore left out (R05 Task 8).
 */
class BackupsTab extends Component
{
    #[Locked]
    public Site $site;

    /** How many of the site's restores the tab lists. */
    private const RESTORES = 10;

    public function render(): View
    {
        return view('filament.sites.backups-tab', [
            'plans' => $this->site->backupPlans()->with('destination')->orderBy('key')->get(),
            'restores' => $this->site->jobs()->where('command', 'backup.restore')->latest('id')->limit(self::RESTORES)->get(),
            'pending' => $this->site->jobs()->where('command', 'backup.restore')->whereNotIn('status', AgentJob::FINISHED)->exists(),
        ]);
    }
}
