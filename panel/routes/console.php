<?php

use Illuminate\Support\Facades\Schedule;

// zimam-panel-scheduler.timer runs schedule:run every minute (§7.1).
Schedule::command('zimam:prune-history')->daily();
