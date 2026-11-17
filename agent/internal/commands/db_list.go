package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// dbList answers db.list: the databases that site states list, with their
// sizes, for one site or for all of them. A database that MySQL lacks is
// still listed, with size 0. MySQL not installed is an empty list, since
// there can be no database; installed but not running is a conflict.
func dbList(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		want := p.(*contract.DbListParams).Site
		if want != "" {
			if _, err := loadDBSite(d, want); err != nil {
				return nil, err
			}
		}
		all, err := sites.All(d.Paths)
		if err != nil {
			return nil, err
		}
		installed, err := mysql.Installed(d)
		if err != nil {
			return nil, err
		}
		res := contract.NewDbListResult()
		if !installed {
			return res, nil
		}
		sizes, err := mysql.Sizes(ctx, d)
		if err != nil {
			return nil, dbError(err)
		}
		for _, s := range all {
			if want != "" && s.Name != want {
				continue
			}
			for _, name := range s.Databases {
				res.Databases = append(res.Databases, contract.DatabaseInfo{Name: name, Site: s.Name, SizeBytes: contract.DatabaseSize(sizes[string(name)])})
			}
		}
		return res, nil
	}
}
