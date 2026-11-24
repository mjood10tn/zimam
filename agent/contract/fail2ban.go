package contract

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The fail2ban commands (v0.6.0 plan, "New agent commands"; R06-D5).
func init() {
	Register(Descriptor{
		Name:    "fail2ban.config",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &Fail2banConfigParams{} },
		Result:  Fail2banStatusResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:   "fail2ban.status",
		Kind:   KindRead,
		Params: func() Params { return &Fail2banStatusParams{} },
		Result: Fail2banStatusResult{},
	})
	Register(Descriptor{
		Name:    "fail2ban.unban",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &Fail2banUnbanParams{} },
		Result:  Fail2banUnbanResult{},
		Subject: func(Params) string { return "server" },
	})
}

// JailName is one of the three jails Zimam renders.
type JailName string

var jailNames = []JailName{"sshd", "zimam-panel", "zimam-site-auth"}

func (j JailName) Validate() error {
	if slices.Contains(jailNames, j) {
		return nil
	}
	return errors.New("must be sshd, zimam-panel or zimam-site-auth")
}

func (JailName) JSONSchema() Schema { return Schema{"type": "string", "enum": jailNames} }

// JailMaxRetry, JailFindTime and JailBanTime are a jail's maxretry, findtime
// and bantime (seconds).
type (
	JailMaxRetry int
	JailFindTime int
	JailBanTime  int
)

func (n JailMaxRetry) Validate() error  { return intRange(int(n), 1, 50) }
func (JailMaxRetry) JSONSchema() Schema { return rangeSchema(1, 50) }
func (n JailFindTime) Validate() error  { return intRange(int(n), 60, 86400) }
func (JailFindTime) JSONSchema() Schema { return rangeSchema(60, 86400) }
func (n JailBanTime) Validate() error   { return intRange(int(n), 60, 2592000) }
func (JailBanTime) JSONSchema() Schema  { return rangeSchema(60, 2592000) }

type Fail2banJail struct {
	Name     JailName     `json:"name"`
	Enabled  bool         `json:"enabled"`
	MaxRetry JailMaxRetry `json:"max_retry"`
	FindTime JailFindTime `json:"find_time"`
	BanTime  JailBanTime  `json:"ban_time"`
}

// Fail2banConfigParams is every jail's settings, rendered together into
// jail.d/zimam.local, and the addresses no jail bans.
type Fail2banConfigParams struct {
	Jails  []Fail2banJail `json:"jails"`
	Ignore IPList         `json:"ignore"`
}

func (p *Fail2banConfigParams) Validate() error {
	f := FieldErrors{}
	if len(p.Jails) != len(jailNames) {
		f["jails"] = "must list sshd, zimam-panel and zimam-site-auth once each"
	}
	seen := map[JailName]bool{}
	for i, j := range p.Jails {
		prefix := fmt.Sprintf("jails.%d.", i)
		f.Check(prefix+"name", j.Name)
		if seen[j.Name] {
			f[prefix+"name"] = "is listed twice"
		}
		seen[j.Name] = true
		f.Check(prefix+"max_retry", j.MaxRetry)
		f.Check(prefix+"find_time", j.FindTime)
		f.Check(prefix+"ban_time", j.BanTime)
	}
	if p.Ignore == nil {
		f["ignore"] = "is required (an empty list for none)"
	} else {
		f.Check("ignore", p.Ignore)
	}
	return f.Err()
}

type Fail2banStatusParams struct{}

func (*Fail2banStatusParams) Validate() error { return nil }

// Fail2banStatusResult is what fail2ban-client reports; Jails lists the
// running ones.
type Fail2banStatusResult struct {
	Installed bool                 `json:"installed"`
	Running   bool                 `json:"running"`
	Jails     []Fail2banJailStatus `json:"jails"`
}

type Fail2banJailStatus struct {
	Name   JailName      `json:"name"`
	Banned []Fail2banBan `json:"banned"`
}

type Fail2banBan struct {
	Address string    `json:"address"`
	Until   time.Time `json:"until"`
}

// BanAddress is one canonical IP address, never a prefix: fail2ban bans
// addresses.
type BanAddress string

func (a BanAddress) Validate() error {
	if strings.Contains(string(a), "/") {
		return errors.New("must be an address, not a prefix")
	}
	p, err := ParseIPEntry(string(a))
	if err != nil {
		return err
	}
	if p.Addr().String() != string(a) {
		return fmt.Errorf("must be written as %s", p.Addr())
	}
	return nil
}

// JSONSchema checks only the shape; Validate parses the address.
func (BanAddress) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": `^[0-9a-f.:]+$`}
}

type Fail2banUnbanParams struct {
	Jail    JailName   `json:"jail"`
	Address BanAddress `json:"address"`
}

func (p *Fail2banUnbanParams) Validate() error {
	f := FieldErrors{}
	f.Check("jail", p.Jail)
	f.Check("address", p.Address)
	return f.Err()
}

// Fail2banUnbanResult says whether the address was banned in the jail.
type Fail2banUnbanResult struct {
	Jail     JailName   `json:"jail"`
	Address  BanAddress `json:"address"`
	Unbanned bool       `json:"unbanned"`
}
