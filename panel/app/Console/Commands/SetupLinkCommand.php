<?php

namespace App\Console\Commands;

use App\Actions\IssueSetupLink;
use App\Settings\PanelSettings;
use Illuminate\Console\Command;

/**
 * Prints a new setup link on the first line and the panel IP allowlist on
 * the second (design §4). Bootstrap and `zimam-agent panel setup-link` run
 * it as zimam and read the link from the first line.
 */
final class SetupLinkCommand extends Command
{
    protected $signature = 'zimam:setup-link {--clear-ip-allowlist : Empty the panel IP allowlist first, when it locks you out}';

    protected $description = 'Issue a one-time setup link; with an admin, the link resets its password and 2FA';

    public function handle(IssueSetupLink $issue, PanelSettings $settings): int
    {
        if ($this->option('clear-ip-allowlist')) {
            $settings->setPanelIpAllowlist([]);
        }
        $allowlist = $settings->panelIpAllowlist();

        $this->line($issue());
        $this->line('Panel IP allowlist: '.($allowlist === [] ? 'empty' : implode(', ', $allowlist)));

        return self::SUCCESS;
    }
}
