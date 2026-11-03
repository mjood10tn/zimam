#!/usr/bin/env bash
# Done-when 5 (design §2, §9.5): a loop requests https://php84.zimam.test/
# every 50 ms while the panel switches php84 to PHP 8.5 and back.
#   m2-switch.sh start   starts the loop in the background
#   m2-switch.sh stop    stops it, then checks it: every response was 200,
#                        both versions answered, and 8.4 answered last
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"
load_state

dir=$E2E_WORK/switch
host=php84.zimam.test

case ${1:-} in
start)
	rm -rf "$dir"
	mkdir -p "$dir"
	# One line per request: the HTTP code (000 when curl got no answer),
	# then the body's version.
	# shellcheck disable=SC2016 # expanded by the inner bash
	setsid nohup bash -c '
		while [[ ! -e $0/stop ]]; do
			code=$(curl -sS -m 10 -o "$0/body" -w "%{http_code}" --resolve "$1:443:$2" --cacert "$3" "https://$1/" 2>>"$0/curl.err") || true
			printf "%s %s\n" "${code:-000}" "$(sed -n "s/.* PHP \([0-9.]*\).*/\1/p" "$0/body" 2>/dev/null)"
			rm -f "$0/body"
			sleep 0.05
		done' "$dir" "$host" "$CIP" "$E2E_WORK/pebble-root.pem" >"$dir/results" 2>"$dir/loop.err" </dev/null &
	echo $! >"$dir/pid"
	sleep 2
	[[ -s $dir/results ]] || die "the request loop isn't answering: $(cat "$dir/loop.err" "$dir/curl.err" 2>/dev/null)"
	log "the request loop runs (pid $(cat "$dir/pid"))"
	;;
stop)
	touch "$dir/stop"
	for _ in $(seq 1 60); do
		kill -0 "$(cat "$dir/pid")" 2>/dev/null || break
		sleep 0.5
	done
	total=$(wc -l <"$dir/results")
	failed=$(awk '$1 != 200' "$dir/results" | wc -l)
	if ((failed > 0)); then
		awk '$1 != 200' "$dir/results" | sort | uniq -c >&2
		cat "$dir/curl.err" >&2 2>/dev/null || true
		die "FAIL done-when 5: $failed of $total requests didn't answer 200"
	fi
	((total >= 100)) || die "FAIL done-when 5: only $total requests ran"
	grep -q ' 8\.5\.' "$dir/results" || die "FAIL done-when 5: php84 never answered on PHP 8.5"
	last=$(tail -n 1 "$dir/results")
	[[ $last == "200 8.4."* ]] || die "FAIL done-when 5: the last answer was: $last"
	echo "PASS  done-when 5: $total requests across the switch to 8.5 and back, all 200 ($(grep -c ' 8\.5\.' "$dir/results") on 8.5)"
	;;
*) die "usage: m2-switch.sh start|stop" ;;
esac
