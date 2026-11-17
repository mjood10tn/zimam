<?php

// The page the e2e run requests: the release marker of the commit that is
// live, and E2E_GREETING from the configuration. ?log=<word> also writes a
// line to storage/logs/laravel.log, the log the panel's viewer reads.
// ?db=<word>, ?cache=<word> and ?queue=<word> use the site's MySQL user and
// Valkey user, and ?done=<word> asks whether a queue worker ran the job
// (app/Services.php); a failure answers 500 with its message.
require __DIR__.'/../vendor/autoload.php';

$app = new App\App(dirname(__DIR__));
header('Content-Type: text/plain; charset=utf-8');

$services = ['db' => 'row', 'cache' => 'cache', 'queue' => 'push', 'done' => 'done'];
foreach ($services as $param => $method) {
    $word = $_GET[$param] ?? null;
    if (is_string($word) && preg_match('/^[A-Za-z0-9-]{1,64}$/', $word) === 1) {
        try {
            echo (new App\Services($app))->$method($word), "\n";
        } catch (Throwable $e) {
            http_response_code(500);
            echo 'error: ', $e->getMessage(), "\n";
        }
        exit;
    }
}

$word = $_GET['log'] ?? null;
if (is_string($word) && preg_match('/^[A-Za-z0-9-]{1,64}$/', $word) === 1) {
    $app->log('info', 'zimam-e2e '.$word);
}
printf("Zimam e2e app release=%s greeting=%s\n", $app->release(), $app->config()['E2E_GREETING'] ?? '');
