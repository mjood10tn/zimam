<?php

namespace App;

/**
 * What the app knows about itself: its folder, its release marker and its
 * configuration. Like Laravel, it reads the cached configuration in
 * bootstrap/cache/config.php when `artisan config:cache` made one, and the
 * .env otherwise, so a changed .env applies from the next deploy.
 */
final class App
{
    public function __construct(public readonly string $base) {}

    /** The marker the scenario commits to release.txt, one per commit. */
    public function release(): string
    {
        return trim((string) @file_get_contents($this->base.'/release.txt'));
    }

    /** @return array<string, string> */
    public function config(): array
    {
        $cached = $this->base.'/bootstrap/cache/config.php';
        if (is_file($cached)) {
            return require $cached;
        }

        return $this->env();
    }

    /**
     * The .env's KEY=VALUE lines; comments, blank lines and anything else
     * are skipped, and one pair of surrounding quotes is dropped.
     *
     * @return array<string, string>
     */
    public function env(): array
    {
        $values = [];
        $lines = @file($this->base.'/.env', FILE_IGNORE_NEW_LINES) ?: [];
        foreach ($lines as $line) {
            if (preg_match('/^([A-Z_][A-Z0-9_]*)=(.*)$/', trim($line), $m) !== 1) {
                continue;
            }
            $value = $m[2];
            if (strlen($value) >= 2 && ($value[0] === '"' || $value[0] === "'") && $value[-1] === $value[0]) {
                $value = substr($value, 1, -1);
            }
            $values[$m[1]] = $value;
        }

        return $values;
    }

    /** Appends one line to storage/logs/laravel.log, in Laravel's format. */
    public function log(string $level, string $message): void
    {
        $env = $this->config()['APP_ENV'] ?? 'production';
        $line = sprintf("[%s] %s.%s: %s\n", gmdate('Y-m-d H:i:s'), $env, strtoupper($level), $message);
        file_put_contents($this->base.'/storage/logs/laravel.log', $line, FILE_APPEND | LOCK_EX);
    }
}
