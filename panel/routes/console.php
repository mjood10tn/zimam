<?php

use Illuminate\Support\Facades\Schedule;

// zimam-panel-scheduler.timer runs schedule:run every minute (§7.1).
Schedule::command('zimam:sample-metrics')->everyMinute();
// Timer runs of backup plans have no panel job to follow (R05-D9).
Schedule::command('zimam:sync-backup-runs')->everyMinute();
Schedule::command('zimam:prune-metrics')->daily();
Schedule::command('zimam:prune-history')->daily();
Schedule::command('zimam:prune-cache')->daily();
Schedule::command('zimam:run-security-checks')->dailyAt('03:30');
