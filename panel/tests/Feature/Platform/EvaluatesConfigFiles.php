<?php

namespace Tests\Feature\Platform;

/**
 * Evaluates a config file the way the server's own environment would, rather
 * than phpunit.xml's: given variables are set, null ones are unset.
 */
trait EvaluatesConfigFiles
{
    /**
     * @param  array<string, string|null>  $env
     * @return array<string, mixed>
     */
    private function evaluateConfig(string $file, array $env): array
    {
        $saved = [];
        foreach ($env as $name => $value) {
            $saved[$name] = [$_ENV[$name] ?? null, $_SERVER[$name] ?? null, getenv($name)];
            if ($value === null) {
                unset($_ENV[$name], $_SERVER[$name]);
                putenv($name);
            } else {
                $_ENV[$name] = $_SERVER[$name] = $value;
                putenv("{$name}={$value}");
            }
        }

        try {
            return require config_path($file);
        } finally {
            foreach ($saved as $name => [$env, $server, $getenv]) {
                unset($_ENV[$name], $_SERVER[$name]);
                putenv($name);
                if ($env !== null) {
                    $_ENV[$name] = $env;
                }
                if ($server !== null) {
                    $_SERVER[$name] = $server;
                }
                if ($getenv !== false) {
                    putenv("{$name}={$getenv}");
                }
            }
        }
    }
}
