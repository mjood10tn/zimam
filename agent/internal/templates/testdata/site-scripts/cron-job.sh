#!/bin/bash
set -eo pipefail
. "$HOME/.zimam/site.env.sh"
cd "$HOME/site/current"
ZIMAM_LOG="$HOME/logs/cron.log"
exec 9>"$HOME/.zimam/cron/backup.lock"
if ! flock -n 9; then
  echo "$(date -Is) [backup] skipped: the previous run is still going" >>"$ZIMAM_LOG"
  exit 0
fi
echo "$(date -Is) [backup] started" >>"$ZIMAM_LOG"
ZIMAM_RC=0
(
php artisan backup:run && echo 'done'
) >>"$ZIMAM_LOG" 2>&1 || ZIMAM_RC=$?
echo "$(date -Is) [backup] finished with status $ZIMAM_RC" >>"$ZIMAM_LOG"
exit "$ZIMAM_RC"
