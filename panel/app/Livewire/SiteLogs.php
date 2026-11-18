<?php

namespace App\Livewire;

use App\Filament\Pages\Logs;
use App\Models\Site;
use Illuminate\Contracts\View\View;
use Livewire\Attributes\Locked;
use Livewire\Component;

/**
 * A site's Logs tab: its three sources (§11.1), plus Laravel's log for a
 * Laravel site (R02-D8), the cron log of any site and the workers' journal
 * for a PHP or Laravel site (v0.4.0), one at a time.
 */
class SiteLogs extends Component
{
    #[Locked]
    public Site $site;

    public string $source = 'site.nginx-access';

    public function render(): View
    {
        $ids = ['site.nginx-access', 'site.nginx-error', 'site.php-error'];
        if ($this->site->isLaravel()) {
            $ids[] = 'site.laravel';
        }
        // A static site may have cron jobs, so it has the log too.
        $ids[] = 'site.cron';
        if ($this->site->runsPhp()) {
            $ids[] = 'site.workers';
        }
        $sources = [];
        foreach ($ids as $id) {
            $sources[$id] = Logs::label($id);
        }
        if (! array_key_exists($this->source, $sources)) {
            $this->source = 'site.nginx-access';
        }

        return view('livewire.site-logs', ['sources' => $sources]);
    }
}
