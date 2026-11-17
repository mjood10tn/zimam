package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/valkey"
)

func init() {
	jobHandlers["valkey.install"] = valkeyInstall
}

// valkeyInstall installs Valkey from Ubuntu's archive. It has no precheck:
// running it again converges, so a server that has Valkey is no conflict.
func valkeyInstall(ctx context.Context, j *jobs.Context, _ contract.Params) (any, error) {
	version, err := valkey.Install(ctx, j.Deps(), cacheUsers(j.Deps().Paths))
	if err != nil {
		return nil, err
	}
	return contract.ServiceInstallResult{Version: version}, nil
}

// cacheUsers lists the Valkey users the ACL file must hold: one for each
// site whose state records a cache user's password hash. valkey.Install calls
// it under the ACL lock. A site state that can't be read is an error, since
// an ACL file written without that site's user would lock the site out.
func cacheUsers(p paths.Paths) func() ([]valkey.SiteUser, error) {
	return func() ([]valkey.SiteUser, error) {
		all, err := sites.All(p)
		if err != nil {
			return nil, err
		}
		var users []valkey.SiteUser
		for _, st := range all {
			if st.CacheUserHash == "" {
				continue
			}
			hash, err := valkey.ParseHash(st.CacheUserHash)
			if err != nil {
				return nil, err
			}
			users = append(users, valkey.SiteUser{Name: st.Name, Hash: hash})
		}
		return users, nil
	}
}
