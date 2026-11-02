#!/usr/bin/env bats
# Step 4 of design §4: the release manifest's signature, its expiry and the
# agent's digest. Every run makes its own throwaway key: no private key is
# committed (global rule 10).

load helpers

setup_file() {
	export KEYS=$BATS_FILE_TMPDIR
	openssl genpkey -algorithm ed25519 -out "$KEYS/key.pem"
	openssl pkey -in "$KEYS/key.pem" -pubout -out "$KEYS/pub.pem"
	openssl genpkey -algorithm ed25519 -out "$KEYS/other.pem"
	head -c 4096 /dev/urandom >"$KEYS/agent"
}

setup() {
	setup_installer
	R=$T/release
}

# release writes a signed release into $R that expires at $1.
release() {
	mkdir -p "$R"
	cp "$KEYS/agent" "$R/zimam-agent-linux-amd64"
	local sum
	sum=$(sha256sum "$R/zimam-agent-linux-amd64" | cut -d' ' -f1)
	printf '{\n  "version": "1.2.3",\n  "protocol": 1,\n  "min_agent_version": "1.2.3",\n  "panel_php_min": "8.3",\n  "expires": "%s",\n  "artifacts": {\n    "zimam-agent-linux-amd64": "sha256:%s"\n  }\n}\n' \
		"$1" "$sum" >"$R/manifest.json"
	openssl pkeyutl -sign -inkey "$KEYS/key.pem" -rawin -in "$R/manifest.json" -out "$R/manifest.json.sig"
}

in_days() {
	date -u -d "$1 days" +%Y-%m-%dT%H:%M:%SZ
}

verify() {
	installer 'RELEASE_PUBLIC_KEY_PEM=$(<"$KEYS/pub.pem"); verify_release "$1" amd64' "$R"
}

@test "a valid release passes" {
	release "$(in_days 30)"
	verify
	[ "$status" -eq 0 ]
}

@test "a tampered manifest fails on its signature" {
	release "$(in_days 30)"
	sed -i 's/"protocol": 1/"protocol": 2/' "$R/manifest.json"
	verify
	[ "$status" -eq 1 ]
	[[ $output == *"The release manifest's signature is not valid."* ]]
}

@test "a tampered signature fails" {
	release "$(in_days 30)"
	openssl pkeyutl -sign -inkey "$KEYS/other.pem" -rawin -in "$R/manifest.json" -out "$R/manifest.json.sig"
	verify
	[ "$status" -eq 1 ]
	[[ $output == *"The release manifest's signature is not valid."* ]]
}

@test "nothing in the manifest is read before its signature passes" {
	release "$(in_days 30)"
	printf 'not json' >>"$R/manifest.json"
	verify
	[ "$status" -eq 1 ]
	[[ $output == *"signature is not valid"* ]]
	[[ $output != *Traceback* ]]
}

@test "an expired manifest fails" {
	release "$(in_days -1)"
	verify
	[ "$status" -eq 1 ]
	[[ $output == *"The release manifest expired on "* ]]
}

@test "an agent binary that doesn't match fails" {
	release "$(in_days 30)"
	printf 'x' >>"$R/zimam-agent-linux-amd64"
	verify
	[ "$status" -eq 1 ]
	[[ $output == *"The agent binary doesn't match the signed manifest."* ]]
}

@test "an architecture the manifest doesn't list fails" {
	release "$(in_days 30)"
	cp "$R/zimam-agent-linux-amd64" "$R/zimam-agent-linux-arm64"
	installer 'RELEASE_PUBLIC_KEY_PEM=$(<"$KEYS/pub.pem"); verify_release "$1" arm64' "$R"
	[ "$status" -eq 1 ]
	[[ $output == *"doesn't list zimam-agent-linux-arm64"* ]]
}
