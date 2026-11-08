#!/usr/bin/env bash
# Done-when 6 (design §2, §8.5), outside the browser: with the default now
# 8.4, php over SSH runs 8.4, and every site still serves its own version.
# The browser checks that the create form starts new sites on 8.4.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"
load_state

out=$(in_c runuser -u ubuntu -- php -r 'echo PHP_VERSION;') || die "FAIL done-when 6: php didn't run as ubuntu"
[[ $out == 8.4.* ]] || die "FAIL done-when 6: php as ubuntu runs $out, want 8.4"
echo "PASS  done-when 6: php over SSH runs $out"

root=$E2E_CA
for site in php84:8.4 php85:8.5; do
	name=${site%%:*} v=${site#*:}
	body=$(curl -fsS -m 10 --resolve "$name.$E2E_DOMAIN:443:$CIP" --cacert "$root" "https://$name.$E2E_DOMAIN/") ||
		die "FAIL done-when 6: $name didn't answer"
	[[ $body == "Zimam site $name is ready. PHP $v."* ]] || die "FAIL done-when 6: $name answered: $body"
	echo "PASS  done-when 6: $name still runs PHP $v"
done
# The contract suite's site was made on the old default.
body=$(in_c curl -fsS -H 'Host: contract-life.example.com' http://127.0.0.1/) || die "FAIL done-when 6: contract-life didn't answer"
[[ $body == "Zimam site contract-life is ready. PHP 8.5."* ]] || die "FAIL done-when 6: contract-life answered: $body"
echo "PASS  done-when 6: contract-life still runs PHP 8.5"
