<?php

use Illuminate\Support\Facades\Schedule;

// zimam-panel-scheduler.timer runs schedule:run every minute (§7.1).
Schedule::command('zimam:sample-metrics')->everyMinute();
Schedule::command('zimam:prune-metrics')->daily();
Schedule::command('zimam:prune-history')->daily();
Schedule::command('zimam:run-security-checks')->dailyAt('03:30');
