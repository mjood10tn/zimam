package sites

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

// drainWait is how long requests already on the old pool get before its
// FPM service stops, when the site's pool was its last. A stop sends
// SIGTERM, which ends FPM's workers mid-request; a reload gives them
// process_control_timeout, and this matches it. A variable, so tests can
// shorten it.
var drainWait = 10 * time.Second

// CheckSwitch is site.php's precheck (§9.5). It answers not_found for a
// site that doesn't exist and for a PHP version that isn't installed,
// conflict for a static site and for the version the site already runs,
// and validation_failed for a version past its end of support.
func CheckSwitch(d sysexec.Deps, p contract.SitePHPParams) error {
	st, err := Load(d.Paths, p.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", p.Name)}
	}
	if err != nil {
		return err
	}
	switch {
	case st.Type != contract.SitePHP:
		return conflict("Site %s is static, so it has no PHP version.", p.Name)
	case st.PHPVersion == p.PHPVersion:
		return conflict("Site %s already runs PHP %s.", p.Name, p.PHPVersion)
	}
	return checkPHP(d, p.PHPVersion)
}

// Switch moves a PHP site to another version without downtime (§9.5): the
// new pool serves before Nginx points at it, and the old pool goes only
// after. If the new pool or the new vhost fails, the site is left as it was
// and the new pool is removed. Each step checks before it acts, so a switch
// that failed at step 3 is finished by sending it again; until then the
// state file keeps the old version.
func Switch(ctx context.Context, d sysexec.Deps, p contract.SitePHPParams) (contract.SitePHPResult, error) {
	if err := p.Validate(); err != nil {
		return contract.SitePHPResult{}, err
	}
	st, err := Load(d.Paths, p.Name)
	if err != nil {
		return contract.SitePHPResult{}, err
	}
	if st.Type != contract.SitePHP {
		return contract.SitePHPResult{}, conflict("Site %s is static, so it has no PHP version.", p.Name)
	}
	next := st
	next.PHPVersion = p.PHPVersion

	// Step 1: the new pool, running and with its socket.
	if err := ensurePool(ctx, d, st.Name, next.PHPVersion, st.Security); err != nil {
		return contract.SitePHPResult{}, undoNewPool(ctx, d, st.Name, next.PHPVersion, err)
	}
	// Step 2: Nginx on the new socket. ApplySite restores the old vhost when
	// nginx -t refuses the new one; after any other failure it is rendered
	// again here.
	if err := ensureVhost(ctx, d, next); err != nil {
		if rerr := ensureVhost(ctx, d, st); rerr != nil {
			err = fmt.Errorf("%w; restoring the old vhost: %v", err, rerr)
		}
		return contract.SitePHPResult{}, undoNewPool(ctx, d, st.Name, next.PHPVersion, err)
	}
	// Step 3: the old pool goes, along with any the site has on a version
	// other than these two, which a cancelled switch can leave.
	for _, r := range contract.PHPAllowlist {
		v := r.Version
		if v == next.PHPVersion || (v != st.PHPVersion && !exists(d.Paths.PHPPool(v, st.Name))) {
			continue
		}
		if err := removePool(ctx, d, st.Name, v, drainWait); err != nil {
			return contract.SitePHPResult{}, fmt.Errorf("site %s now runs on PHP %s, but removing its PHP %s pool failed, so the switch isn't finished; switch to PHP %s again to finish it: %w", st.Name, next.PHPVersion, v, next.PHPVersion, err)
		}
	}
	// Step 4.
	if err := Save(d.Paths, next); err != nil {
		return contract.SitePHPResult{}, err
	}
	d.Logf("Site %s now runs on PHP %s", st.Name, next.PHPVersion)
	return contract.SitePHPResult{Name: st.Name, PHPVersion: next.PHPVersion}, nil
}

// undoNewPool removes the pool step 1 wrote, after cause.
func undoNewPool(ctx context.Context, d sysexec.Deps, name contract.SiteName, v contract.PHPVersion, cause error) error {
	if err := removePool(ctx, d, name, v, 0); err != nil {
		return fmt.Errorf("%w; removing the new PHP %s pool: %v", cause, v, err)
	}
	return cause
}

// removePool removes a site's pool on v, if it is there, then brings v's
// FPM service in line: reloaded while other pools remain, otherwise stopped
// and disabled after drain. It holds v's job lock meanwhile.
func removePool(ctx context.Context, d sysexec.Deps, name contract.SiteName, v contract.PHPVersion, drain time.Duration) error {
	unlock, err := sysexec.Lock(ctx, d.Paths.SubjectLock("php_"+string(v)))
	if err != nil {
		return err
	}
	defer unlock()
	if path := d.Paths.PHPPool(v, name); exists(path) {
		d.Logf("Removing %s's PHP %s pool", name, v)
		err := configtx.WithReloadLock(ctx, d.Paths, func() error {
			return configtx.Apply(ctx, []configtx.Change{{Path: path, Remove: true}}, func(ctx context.Context) error {
				return testPools(ctx, d, v)
			})
		})
		if err != nil {
			return err
		}
	}
	pools, err := php.HasPools(d, v)
	if err != nil {
		return err
	}
	if !pools && drain > 0 {
		d.Logf("Giving requests on the PHP %s pool %s to finish", v, drain)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(drain):
		}
	}
	return php.ApplyFPM(ctx, d, v)
}
