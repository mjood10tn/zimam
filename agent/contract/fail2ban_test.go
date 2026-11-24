package contract

import "testing"

func jails() []Fail2banJail {
	return []Fail2banJail{
		{Name: "sshd", Enabled: true, MaxRetry: 5, FindTime: 600, BanTime: 3600},
		{Name: "zimam-panel", Enabled: true, MaxRetry: 5, FindTime: 600, BanTime: 3600},
		{Name: "zimam-site-auth", Enabled: false, MaxRetry: 10, FindTime: 600, BanTime: 600},
	}
}

func TestFail2banValues(t *testing.T) {
	checkValues(t, []JailName{"sshd", "zimam-panel", "zimam-site-auth"}, []JailName{"", "nginx-http-auth", "SSHD", "recidive", "sshd "})
	checkValues(t, []JailMaxRetry{1, 50}, []JailMaxRetry{0, 51})
	checkValues(t, []JailFindTime{60, 86400}, []JailFindTime{59, 86401})
	checkValues(t, []JailBanTime{60, 2592000}, []JailBanTime{59, 2592001})
	checkValuesShape(t,
		[]BanAddress{"192.0.2.50", "2001:db8::50"},
		[]BanAddress{"", "192.0.2.0/24", "2001:db8::/64", "2001:DB8::50", "::ffff:192.0.2.1", "fe80::1%eth0", "192.0.2.050", "a b", "192.0.2.50\n"})
}

func TestFail2banConfigParams(t *testing.T) {
	ok := Fail2banConfigParams{Jails: jails(), Ignore: IPList{"203.0.113.0/24"}}
	if err := ok.Validate(); err != nil {
		t.Error(err)
	}
	mod := func(f func(*Fail2banConfigParams)) Fail2banConfigParams {
		p := Fail2banConfigParams{Jails: jails(), Ignore: IPList{}}
		f(&p)
		return p
	}
	for name, p := range map[string]Fail2banConfigParams{
		"unknown jail":   mod(func(p *Fail2banConfigParams) { p.Jails[2].Name = "recidive" }),
		"duplicate jail": mod(func(p *Fail2banConfigParams) { p.Jails[2].Name = "sshd" }),
		"missing jail":   mod(func(p *Fail2banConfigParams) { p.Jails = p.Jails[:2] }),
		"extra jail":     mod(func(p *Fail2banConfigParams) { p.Jails = append(p.Jails, p.Jails[0]) }),
		"max retry":      mod(func(p *Fail2banConfigParams) { p.Jails[0].MaxRetry = 0 }),
		"find time":      mod(func(p *Fail2banConfigParams) { p.Jails[0].FindTime = 30 }),
		"ban time":       mod(func(p *Fail2banConfigParams) { p.Jails[0].BanTime = 2592001 }),
		"nil ignore":     mod(func(p *Fail2banConfigParams) { p.Ignore = nil }),
		"bad ignore":     mod(func(p *Fail2banConfigParams) { p.Ignore = IPList{"203.0.113.5/24"} }),
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, p)
		}
	}
}

func TestFail2banUnbanParams(t *testing.T) {
	if err := (&Fail2banUnbanParams{Jail: "zimam-panel", Address: "192.0.2.50"}).Validate(); err != nil {
		t.Error(err)
	}
	for _, p := range []Fail2banUnbanParams{{Jail: "nope", Address: "192.0.2.50"}, {Jail: "sshd", Address: "192.0.2.0/24"}, {Jail: "sshd"}} {
		if p.Validate() == nil {
			t.Errorf("accepted %+v", p)
		}
	}
}
