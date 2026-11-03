package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/logpolicy"
	"zimam/agent/internal/sites"
)

func init() {
	jobHandlers["logs.policy"] = logsPolicy
}

// logsPolicy stores the retention periods and the per-site cap in
// agent.json, then re-renders every rotation rule and the journald drop-in
// and restarts journald (§11.4). Maintenance reads the cap and the job
// retention from agent.json on its next run.
func logsPolicy(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.LogsPolicyParams)
	d := j.Deps()
	cfg, err := config.Load(d.Paths)
	if err != nil {
		return nil, err
	}
	cfg.LogRetentionDays, cfg.AuditRetentionDays, cfg.SiteLogCapBytes = params.RetentionDays, params.AuditRetentionDays, params.SiteCapBytes
	all, err := sites.All(d.Paths)
	if err != nil {
		return nil, err
	}
	names := make([]contract.SiteName, 0, len(all))
	for _, st := range all {
		names = append(names, st.Name)
	}
	d.Logf("Keeping logs %d days, the audit log %d days, and at most %d bytes of logs per site", params.RetentionDays, params.AuditRetentionDays, params.SiteCapBytes)
	if err := config.Save(d.Paths, cfg); err != nil {
		return nil, err
	}
	d.Config = cfg
	if err := logpolicy.Apply(ctx, d, logpolicy.Policy{RetentionDays: params.RetentionDays, AuditRetentionDays: params.AuditRetentionDays, SiteCapBytes: params.SiteCapBytes}, names); err != nil {
		return nil, err
	}
	return contract.LogsPolicyResult(params), nil
}
