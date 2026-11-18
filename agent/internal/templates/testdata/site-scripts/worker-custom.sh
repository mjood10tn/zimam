#!/bin/bash
set -eo pipefail
. "$HOME/.zimam/site.env.sh"
cd "$HOME/site/current"
node server.js | tee -a "$HOME/logs/node.log"
