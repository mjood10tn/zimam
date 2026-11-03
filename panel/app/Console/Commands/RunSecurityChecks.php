<?php

namespace App\Console\Commands;

use App\Actions\SubmitAgentCommand;
use App\Agent\Commands\SecurityCheck;
use App\Agent\Exceptions\AgentException;
use App\Models\Site;
use Filament\Support\Exceptions\Halt;
use Illuminate\Console\Command;

/** Submits the server's security checks and every active site's (§7.1, §9.7). Scheduled daily at 03:30. */
final class RunSecurityChecks extends Command
{
    protected $signature = 'zimam:run-security-checks {--server : Only the server\'s checks} {--site=* : Only these sites}';

    protected $description = 'Run the server’s and every site’s security checks';

    public function handle(): int
    {
        $failed = 0;
        foreach (self::commands((bool) $this->option('server'), array_values((array) $this->option('site'))) as $command) {
            $what = $command->site ?? 'server';
            try {
                app(SubmitAgentCommand::class)->run($command, $command->site === null ? null : Site::query()->where('name', $command->site)->first());
                $this->line("Submitted the checks for {$what}.");
            } catch (Halt|AgentException $e) {
                $failed++;
                $this->error("The checks for {$what} weren't submitted: ".($e->getMessage() ?: 'the agent didn’t answer'));
            }
        }

        return $failed === 0 ? self::SUCCESS : self::FAILURE;
    }

    /**
     * The server's checks, then each active site's; with options, only those.
     *
     * @param  list<string>  $sites
     * @return list<SecurityCheck>
     */
    public static function commands(bool $serverOnly = false, array $sites = []): array
    {
        $commands = $sites === [] ? [new SecurityCheck] : [];
        if ($serverOnly) {
            return $commands;
        }
        $query = Site::query()->where('status', Site::ACTIVE)->orderBy('name');
        if ($sites !== []) {
            $query->whereIn('name', $sites);
        }
        foreach ($query->pluck('name') as $name) {
            $commands[] = new SecurityCheck($name);
        }

        return $commands;
    }
}
