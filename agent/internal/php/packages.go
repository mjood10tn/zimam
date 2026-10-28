// Package php installs PHP versions from packages.sury.org (design §8): the
// APT repository, the packages, Zimam's baseline settings, and each
// version's FPM service.
package php

import (
	"slices"

	"zimam/agent/contract"
)

// opcacheSeparate are the versions whose OPcache is a package of its own.
// It is built into PHP from 8.5 (§8.1).
var opcacheSeparate = []contract.PHPVersion{"8.2", "8.3", "8.4"}

// redisPublished are the versions sury publishes php<v>-redis for, checked
// on 2026-10-27 (not yet for 8.6).
var redisPublished = []contract.PHPVersion{"8.2", "8.3", "8.4", "8.5"}

// Packages lists what php.install installs for v (§8.1). It never names the
// bare php<v> package, whose first alternative is Apache's module.
func Packages(v contract.PHPVersion) []string {
	var names []string
	for _, ext := range []string{"fpm", "cli", "common", "mysql", "sqlite3", "mbstring", "xml", "curl", "zip", "gd", "intl", "bcmath", "readline"} {
		names = append(names, "php"+string(v)+"-"+ext)
	}
	if slices.Contains(opcacheSeparate, v) {
		names = append(names, "php"+string(v)+"-opcache")
	}
	if slices.Contains(redisPublished, v) {
		names = append(names, "php"+string(v)+"-redis")
	}
	return names
}
