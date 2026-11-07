//go:build e2e

package commands

import (
	"context"
	"runtime"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
)

// e2e.memhog is a job that touches 512 MiB under a 64M MemoryMax, built
// into e2e agents alone, for §14.4's job-limit probe: its unit must end
// with oom-kill. Like e2e.sleep, its descriptor lives here, so schemagen
// and the contract tests never see it.

func init() {
	contract.Register(contract.Descriptor{
		Name:    "e2e.memhog",
		Kind:    contract.KindJob,
		Audited: true,
		Params:  func() contract.Params { return &E2EMemhogParams{} },
		Result:  E2EMemhogResult{},
		Subject: func(contract.Params) string { return "e2e:memhog" },
		E2EOnly: true,
	})
	jobHandlers["e2e.memhog"] = e2eMemhog
}

// memhogBytes is how much e2e.memhog touches; the unit test lowers it.
var memhogBytes = 512 << 20

// E2EMemhogParams is e2e.memhog's params: none.
type E2EMemhogParams struct{}

func (p *E2EMemhogParams) Validate() error { return nil }

type E2EMemhogResult struct {
	Touched int `json:"touched"` // pages written
}

// e2eMemhog writes one byte in every 4 KiB page, so each page is backed
// by memory: a fresh allocation alone is zero pages the kernel never
// commits. The slice stays live until the end, so nothing frees it early.
func e2eMemhog(ctx context.Context, j *jobs.Context, _ contract.Params) (any, error) {
	j.Logf("Touching %d MiB", memhogBytes>>20)
	b := make([]byte, memhogBytes)
	touched := 0
	for i := 0; i < len(b); i += 4096 {
		if i%(64<<20) == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		b[i] = 1
		touched++
	}
	runtime.KeepAlive(b)
	j.Logf("Touched %d pages; the limit didn't stop it", touched)
	return E2EMemhogResult{Touched: touched}, nil
}
