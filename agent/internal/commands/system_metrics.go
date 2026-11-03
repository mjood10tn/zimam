package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/metrics"
	"zimam/agent/internal/sysexec"
)

// systemMetrics answers system.metrics (§7.5), which the panel asks every
// minute and every 5 s while the Monitoring page is open. Like every read,
// it is never audited (§5.8).
func systemMetrics(d sysexec.Deps) Handler {
	return func(ctx context.Context, _ contract.Params) (any, error) {
		return metrics.Collect(ctx, d)
	}
}
