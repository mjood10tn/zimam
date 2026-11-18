<?php

namespace App;

/**
 * The artisan commands Zimam's Laravel deploy preset runs, doing what
 * Laravel's do to the release, without Laravel: the e2e run needs no
 * packages from Packagist.
 */
final class Console
{
    public function __construct(private readonly App $app) {}

    /** @param list<string> $args */
    public function run(array $args): int
    {
        $command = $args[0] ?? 'list';
        $base = $this->app->base;
        switch ($command) {
            case 'storage:link':
                // public/storage -> ../storage/app/public, relative, as Laravel's --relative.
                $link = $base.'/public/storage';
                if (! is_link($link) && ! symlink('../storage/app/public', $link)) {
                    return $this->fail('The [public/storage] link could not be created.');
                }
                echo "The [public/storage] link has been connected to [storage/app/public].\n";

                return 0;
            case 'migrate':
                if (($this->app->config()['APP_ENV'] ?? 'production') === 'production' && ! in_array('--force', $args, true)) {
                    return $this->fail('The application is in production: run migrate with --force.');
                }
                echo "Nothing to migrate.\n";

                return 0;
            case 'config:cache':
                @unlink($base.'/bootstrap/cache/config.php');
                $this->write('bootstrap/cache/config.php', '<?php return '.var_export($this->app->env(), true).";\n");
                echo "Configuration cached successfully.\n";

                return 0;
            case 'route:cache':
                $this->write('bootstrap/cache/routes-v7.php', "<?php return ['/' => 'public/index.php'];\n");
                echo "Routes cached successfully.\n";

                return 0;
            case 'view:cache':
                $this->write('storage/framework/views/'.$this->app->release().'.php', "<?php // compiled\n");
                echo "Blade templates cached successfully.\n";

                return 0;
            case 'schedule:run':
                // What the scheduler's every-minute run does here: one line in
                // ~/e2e/schedule.log with the PHP version and the release that
                // ran it, so e2e/scenarios/v04-workers.sh sees cron start it
                // and which code it ran.
                $line = sprintf("%s php=%s release=%s\n", gmdate('Y-m-d\TH:i:s\Z'), PHP_VERSION, $base);
                if (file_put_contents(Services::outputDir().'/schedule.log', $line, FILE_APPEND | LOCK_EX) === false) {
                    return $this->fail('The schedule log could not be written.');
                }
                echo "Ran the scheduled e2e command.\n";

                return 0;
            case 'queue:work':
                // The Redis queue worker, without Laravel: it runs until stopped, and
                // ends with the error when Valkey closes its connection. The
                // connection name and options a Zimam worker passes after the
                // command are taken and ignored.
                try {
                    return (new Services($this->app))->work(in_array('--once', $args, true));
                } catch (\Throwable $e) {
                    return $this->fail('The worker stopped: '.$e->getMessage());
                }
            default:
                return $this->fail("Command \"{$command}\" is not defined.");
        }
    }

    private function write(string $path, string $content): void
    {
        if (file_put_contents($this->app->base.'/'.$path, $content) === false) {
            throw new \RuntimeException("{$path} could not be written");
        }
    }

    private function fail(string $message): int
    {
        fwrite(STDERR, $message."\n");

        return 1;
    }
}
