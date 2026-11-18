#!/bin/bash
set -eo pipefail
. "$HOME/.zimam/site.env.sh"
cd "$HOME/site/current"
exec 'php' 'artisan' 'queue:work' 'redis' '--queue=high,default' '--tries=3' '--timeout=0' '--memory=512'
