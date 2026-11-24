package contract

import "testing"

func sshSettings() SSHSettingsParams {
	return SSHSettingsParams{PasswordAuth: false, RootLogin: "prohibit-password", Ports: []TCPPort{22}, MaxAuthTries: 6, LoginGraceSeconds: 120}
}

func TestSSHValues(t *testing.T) {
	checkValues(t, []RootLogin{"no", "prohibit-password"}, []RootLogin{"", "yes", "without-password", "forced-commands-only", "No"})
	checkValues(t, []MaxAuthTries{1, 6, 10}, []MaxAuthTries{0, 11, -1})
	checkValues(t, []LoginGraceSeconds{10, 120, 600}, []LoginGraceSeconds{0, 9, 601})
}

func TestSSHSettingsParams(t *testing.T) {
	mod := func(f func(*SSHSettingsParams)) SSHSettingsParams { p := sshSettings(); f(&p); return p }
	for name, p := range map[string]SSHSettingsParams{
		"default":    sshSettings(),
		"four ports": mod(func(p *SSHSettingsParams) { p.Ports = []TCPPort{22, 2222, 2200, 65535} }),
		"root no":    mod(func(p *SSHSettingsParams) { p.RootLogin, p.PasswordAuth = "no", true }),
	} {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, p := range map[string]SSHSettingsParams{
		"root yes":        mod(func(p *SSHSettingsParams) { p.RootLogin = "yes" }),
		"no ports":        mod(func(p *SSHSettingsParams) { p.Ports = []TCPPort{} }),
		"five ports":      mod(func(p *SSHSettingsParams) { p.Ports = []TCPPort{22, 2222, 2200, 2201, 2202} }),
		"duplicate port":  mod(func(p *SSHSettingsParams) { p.Ports = []TCPPort{22, 22} }),
		"port 0":          mod(func(p *SSHSettingsParams) { p.Ports = []TCPPort{0} }),
		"web port":        mod(func(p *SSHSettingsParams) { p.Ports = []TCPPort{443} }),
		"max auth tries":  mod(func(p *SSHSettingsParams) { p.MaxAuthTries = 11 }),
		"login grace":     mod(func(p *SSHSettingsParams) { p.LoginGraceSeconds = 5 }),
		"zero auth tries": mod(func(p *SSHSettingsParams) { p.MaxAuthTries = 0 }),
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}
