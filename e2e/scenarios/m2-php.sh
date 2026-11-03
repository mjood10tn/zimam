#!/usr/bin/env bash
# Done-when 3 (design §2, §8.3), outside the browser: PHP 8.4 runs beside
# the default 8.5, which is still what php runs until done-when 6.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

for bin in php8.4 php-fpm8.4; do
	out=$(in_c "$bin" -v) || die "FAIL done-when 3: $bin doesn't run"
	[[ $out == "PHP 8.4."* ]] || die "FAIL done-when 3: $bin -v says: ${out%%$'\n'*}"
done
echo "PASS  done-when 3: php8.4 and php-fpm8.4 run"
out=$(in_c php -r 'echo PHP_VERSION;')
[[ $out == 8.5.* ]] || die "FAIL done-when 3: php runs $out, want the default 8.5 still"
echo "PASS  done-when 3: php still runs the default, $out"
