#!/usr/bin/env bash
# The contract suite against the real agent (design §6): the panel copy
# build.sh made with its dev dependencies goes to /home/zimam/contract-test,
# owned by zimam, and PHPUnit's Contract suite runs there as zimam on the
# panel's PHP. A skipped test fails the run, so the socket scenarios can't
# pass by not running.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

log "copying the panel with its dev dependencies to /home/zimam/contract-test"
tar -C "$E2E_WORK" -czf "$E2E_WORK/contract-test.tar.gz" contract-test
lxc file push -q "$E2E_WORK/contract-test.tar.gz" "$E2E_CONTAINER/root/contract-test.tar.gz"
in_c bash -c 'rm -rf /home/zimam/contract-test &&
	tar -C /home/zimam -xzf /root/contract-test.tar.gz &&
	chown -R zimam:zimam /home/zimam/contract-test &&
	rm /root/contract-test.tar.gz'
php=$(in_c python3 -c 'import json; print(json.load(open("/etc/zimam/agent.json"))["panel_php"])')
log "phpunit tests/Contract as zimam on PHP $php"
AS_CWD=/home/zimam/contract-test bash "$E2E_ROOT/e2e/scripts/as.sh" zimam \
	env ZIMAM_AGENT_SOCKET=/run/zimam/agent.sock "php$php" vendor/bin/phpunit --fail-on-skipped tests/Contract
echo "PASS  the contract suite against the real agent"
