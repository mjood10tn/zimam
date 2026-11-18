#!/bin/bash
set -eo pipefail
. "$HOME/.zimam/site.env.sh"
cd "$HOME/site/current"
ZIMAM_LOG="$HOME/logs/cron.log"
exec 9>"$HOME/.zimam/cron/scheduler.lock"
if ! flock -n 9; then
  echo "$(date -Is) [scheduler] skipped: the previous run is still going" >>"$ZIMAM_LOG"
  exit 0
fi
echo "$(date -Is) [scheduler] started" >>"$ZIMAM_LOG"
ZIMAM_RC=0
(
php artisan schedule:run
) >>"$ZIMAM_LOG" 2>&1 || ZIMAM_RC=$?
echo "$(date -Is) [scheduler] finished with status $ZIMAM_RC" >>"$ZIMAM_LOG"
exit "$ZIMAM_RC"
