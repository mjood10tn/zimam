#!/bin/bash
set -eo pipefail
. "$HOME/.zimam/site.env.sh"
cd "$HOME/site/current"
ZIMAM_LOG="$HOME/logs/cron.log"
echo "$(date -Is) [scheduler] started" >>"$ZIMAM_LOG"
ZIMAM_RC=0
(
php artisan schedule:run
) >>"$ZIMAM_LOG" 2>&1 || ZIMAM_RC=$?
echo "$(date -Is) [scheduler] finished with status $ZIMAM_RC" >>"$ZIMAM_LOG"
exit "$ZIMAM_RC"
