<?php

// The page the e2e run requests: the release marker of the commit that is
// live, and E2E_GREETING from the configuration. ?log=<word> also writes a
// line to storage/logs/laravel.log, the log the panel's viewer reads.
require __DIR__.'/../vendor/autoload.php';

$app = new App\App(dirname(__DIR__));
$word = $_GET['log'] ?? null;
if (is_string($word) && preg_match('/^[A-Za-z0-9-]{1,64}$/', $word) === 1) {
    $app->log('info', 'zimam-e2e '.$word);
}
header('Content-Type: text/plain; charset=utf-8');
printf("Zimam e2e app release=%s greeting=%s\n", $app->release(), $app->config()['E2E_GREETING'] ?? '');
