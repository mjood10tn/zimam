<?php

namespace App\Filament\Pages;

use App\Agent\ServerStatus;
use App\Support\PanelLog;
use BackedEnum;
use Filament\Pages\Page;
use Filament\Support\Icons\Heroicon;
use Livewire\Attributes\Url;

/** The server's log sources (§7.3, §11.1), one at a time, and the panel's own log. */
class Logs extends Page
{
    protected static string|BackedEnum|null $navigationIcon = Heroicon::OutlinedDocumentText;

    protected static ?int $navigationSort = 4;

    protected string $view = 'filament.pages.logs';

    #[Url]
    public string $source = 'journal.system';

    public static function getNavigationLabel(): string
    {
        return __('zimam.logs.title');
    }

    public function getTitle(): string
    {
        return __('zimam.logs.title');
    }

    /** @return array<string, string> the server's sources, by id, labelled */
    public static function serverSources(): array
    {
        $ids = ['journal.system', 'journal.kernel', 'journal.ssh', 'journal.nginx'];
        foreach (ServerStatus::current()?->data['php']['installed'] ?? [] as $php) {
            $ids[] = 'journal.php-'.$php['version'];
        }
        array_push($ids, 'journal.zimam-agent', 'journal.zimam-panel', 'file.nginx-error', 'file.certbot', 'file.apt-history', 'file.unattended-upgrades', 'file.zimam-audit', PanelLog::SOURCE);

        return array_combine($ids, array_map(fn (string $id): string => self::label($id), $ids));
    }

    public static function label(string $id): string
    {
        if (preg_match('/^journal\.php-(\d+\.\d+)\z/', $id, $m)) {
            return __('zimam.logs.sources.journal.php', ['version' => $m[1]]);
        }

        return __('zimam.logs.sources.'.$id);
    }

    /** @return array<string, mixed> */
    protected function getViewData(): array
    {
        $sources = self::serverSources();
        if (! array_key_exists($this->source, $sources)) {
            $this->source = 'journal.system';
        }

        return ['sources' => $sources];
    }
}
