<?php

namespace App\Livewire;

use App\Filament\Pages\Logs;
use App\Models\Site;
use Illuminate\Contracts\View\View;
use Livewire\Component;

/** A site's Logs tab: its three sources (§11.1), one at a time. */
class SiteLogs extends Component
{
    public Site $site;

    public string $source = 'site.nginx-access';

    public function render(): View
    {
        $sources = [];
        foreach (['site.nginx-access', 'site.nginx-error', 'site.php-error'] as $id) {
            $sources[$id] = Logs::label($id);
        }
        if (! array_key_exists($this->source, $sources)) {
            $this->source = 'site.nginx-access';
        }

        return view('livewire.site-logs', ['sources' => $sources]);
    }
}
