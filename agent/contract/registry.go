package contract

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Kind is how a command runs (§5.3).
type Kind string

const (
	KindRead   Kind = "read"   // answers directly
	KindJob    Kind = "job"    // runs in its own systemd unit
	KindDirect Kind = "direct" // changes state but answers directly (job.cancel)
)

// Params is a command's parameters, decoded strictly before Validate runs.
type Params interface{ Validate() error }

// Descriptor is one row of §5.3.
type Descriptor struct {
	Name    string
	Kind    Kind
	Audited bool
	Params  func() Params       // new zero value
	Result  any                 // zero value, used for schema generation
	Subject func(Params) string // jobs only: "server", "php:8.4", "site:shop", …
	E2EOnly bool                // never in schemas or release builds
}

// commandNameRe keeps names usable as schema file names.
var commandNameRe = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9_]*)+$`)

var (
	registryMu sync.RWMutex
	registry   = map[string]Descriptor{}
)

// Register adds a command. Commands register from init functions, so a
// malformed or duplicate descriptor is a programming error and panics.
func Register(d Descriptor) {
	if err := d.check(); err != nil {
		panic("contract: " + err.Error())
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[d.Name]; dup {
		panic("contract: " + d.Name + " is registered twice")
	}
	registry[d.Name] = d
}

func (d Descriptor) check() error {
	switch {
	case !commandNameRe.MatchString(d.Name):
		return fmt.Errorf("command name %q is malformed", d.Name)
	case d.Kind != KindRead && d.Kind != KindJob && d.Kind != KindDirect:
		return fmt.Errorf("%s: unknown kind %q", d.Name, d.Kind)
	case d.Params == nil || d.Result == nil:
		return fmt.Errorf("%s: Params and Result are required", d.Name)
	case reflect.TypeOf(d.Params()).Kind() != reflect.Pointer:
		return fmt.Errorf("%s: Params must return a pointer, for decoding into", d.Name)
	case (d.Kind == KindJob) != (d.Subject != nil):
		return errors.New(d.Name + ": jobs, and only jobs, have a Subject")
	}
	return nil
}

func Lookup(name string) (Descriptor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := registry[name]
	return d, ok
}

// All returns every registered command, sorted by name.
func All() []Descriptor {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return slices.SortedFunc(maps.Values(registry), func(a, b Descriptor) int {
		return strings.Compare(a.Name, b.Name)
	})
}
