package siteexec

import (
	"zimam/agent/contract"
	"zimam/agent/internal/helper"
)

// Program is one of the fixed programs the helper runs as a site user
// (R02-D1). The helper owns the table; this names it for the agent.
type Program string

const (
	Git          Program = "git"
	Composer     Program = "composer"
	PHP          Program = "php"
	Artisan      Program = "artisan"
	SSHKeygen    Program = "ssh-keygen"
	DeployScript Program = "deploy-script"
)

// Programs is the whole set.
var Programs = []Program{Git, Composer, PHP, Artisan, SSHKeygen, DeployScript}

// Path is the absolute path the helper executes for p: /usr/bin/git,
// /usr/bin/ssh-keygen, /bin/bash for deploy-script, and /usr/bin/php<v> for
// php, artisan and composer (composer runs the pinned phar, paths.ComposerPhar, artisan the
// release's artisan file). The PHP programs need a supported version; the
// others ignore it.
func (p Program) Path(phpVersion contract.PHPVersion) (string, error) {
	path, _, err := helper.ProgramArgv(string(p), phpVersion)
	return path, err
}
