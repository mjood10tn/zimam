package deploy

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
)

// site.env.read and site.env.write (R02-D10), through the helper as the
// site user, in a real home.

const testEnv = "APP_KEY=base64:dummy-not-a-real-key\nDB_PASSWORD=dummy-secret\n"

func (h *localHelper) Read(ctx context.Context, a helper.Account, name string, offset, limit int64) (helper.ReadResult, error) {
	var r helper.ReadResult
	err := h.serve(ctx, a, "read", nil, &r, name, strconv.FormatInt(offset, 10), strconv.FormatInt(limit, 10))
	return r, err
}

func (e *deployEnv) readEnv() (contract.SiteEnvReadResult, error) {
	e.t.Helper()
	return ReadEnv(context.Background(), e.d, e.h, e.lookup, "shop")
}

func (e *deployEnv) writeEnv(content string) (contract.SiteNameResult, error) {
	e.t.Helper()
	return WriteEnv(context.Background(), e.d, e.h, e.lookup, contract.SiteEnvWriteParams{Name: "shop", Content: contract.EnvContent(content)})
}

func (e *deployEnv) writeShared(content string, mode os.FileMode) {
	e.t.Helper()
	if err := os.MkdirAll(e.home("site/shared"), 0o750); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.home("site/shared/.env"), []byte(content), mode); err != nil {
		e.t.Fatal(err)
	}
}

// noSecretIn fails when the job log or err quotes any line of the .env.
func (e *deployEnv) noSecretIn(err error) {
	e.t.Helper()
	for _, s := range []string{"dummy-secret", "dummy-not-a-real-key"} {
		if err != nil && strings.Contains(err.Error(), s) {
			e.t.Errorf("the error quotes the .env: %v", err)
		}
		for _, l := range e.logs {
			if strings.Contains(l, s) {
				e.t.Errorf("the job log quotes the .env: %q", l)
			}
		}
	}
}

func TestEnvReadReturnsSharedEnv(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	e.writeShared(testEnv, 0o600)
	res, err := e.readEnv()
	wantNoErr(t, err)
	if res.Content != testEnv {
		t.Errorf("content = %q, want %q", res.Content, testEnv)
	}
	if !slices.Contains(e.h.ops, "read site/shared/.env 0 65537") {
		t.Errorf("ops = %v, want a helper read of site/shared/.env capped just over 64 KiB", e.h.ops)
	}
	e.noSecretIn(nil)
}

// Any site with a shared .env has one to read, whatever its type; a site
// without one, or no site at all, is not_found.
func TestEnvReadMissingIsNotFound(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	_, err := e.readEnv()
	wantAgentCode(t, err, contract.ErrNotFound)

	e.writeShared(testEnv, 0o600)
	res, err := e.readEnv()
	wantNoErr(t, err)
	if res.Content != testEnv {
		t.Errorf("a php site's shared .env: content = %q", res.Content)
	}

	_, err = ReadEnv(context.Background(), e.d, e.h, e.lookup, "nosuch")
	wantAgentCode(t, err, contract.ErrNotFound)
}

func TestEnvReadOverCapRefused(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	big := "A=" + strings.Repeat("x", contract.MaxTextFileBytes) + "\n"
	e.writeShared(big, 0o600)
	_, err := e.readEnv()
	ae := wantAgentCodeOf(t, err, contract.ErrConflict)
	if !strings.Contains(ae.Message, "64 KiB") {
		t.Errorf("message = %q, want it to name the 64 KiB cap", ae.Message)
	}
	if strings.Contains(ae.Message, "xxxx") {
		t.Errorf("message quotes the file: %q", ae.Message)
	}

	// Exactly 64 KiB is fine.
	exact := "A=" + strings.Repeat("x", contract.MaxTextFileBytes-3) + "\n"
	e.writeShared(exact, 0o600)
	res, err := e.readEnv()
	wantNoErr(t, err)
	if len(res.Content) != contract.MaxTextFileBytes {
		t.Errorf("read %d bytes, want %d", len(res.Content), contract.MaxTextFileBytes)
	}
}

// A file the panel couldn't save back (a NUL, invalid UTF-8) isn't handed
// out, and the refusal doesn't quote it.
func TestEnvReadRefusesUnsavableContent(t *testing.T) {
	for name, content := range map[string]string{
		"nul":     "A=dummy-secret\x00\n",
		"invalid": "A=dummy-secret\xff\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := newDeployEnv(t, contract.SiteLaravel)
			e.writeShared(content, 0o600)
			_, err := e.readEnv()
			wantAgentCodeOf(t, err, contract.ErrConflict)
			e.noSecretIn(err)
		})
	}
}

// A planted shared/.env symlink is refused, wherever it points: out of the
// home (os.Root would refuse it too) or at another file in it.
func TestEnvReadRefusesSymlink(t *testing.T) {
	for name, target := range map[string]string{
		"outside": "/etc/passwd",
		"inside":  "../../.ssh/id_ed25519",
	} {
		t.Run(name, func(t *testing.T) {
			e := newDeployEnv(t, contract.SiteLaravel)
			if err := os.MkdirAll(e.home("site/shared"), 0o750); err != nil {
				t.Fatal(err)
			}
			os.MkdirAll(e.home(".ssh"), 0o700)
			os.WriteFile(e.home(".ssh/id_ed25519"), []byte("dummy-secret"), 0o600)
			if err := os.Symlink(target, e.home("site/shared/.env")); err != nil {
				t.Fatal(err)
			}
			res, err := e.readEnv()
			wantAgentCodeOf(t, err, contract.ErrConflict)
			if res.Content != "" {
				t.Errorf("content = %q, want none", res.Content)
			}
			for _, op := range e.h.ops {
				if strings.HasPrefix(op, "read ") {
					t.Errorf("the link was read: %v", e.h.ops)
				}
			}
		})
	}
}

func TestEnvWriteMode0600ThroughHelper(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	e.writeShared("OLD=1\n", 0o640)
	res, err := e.writeEnv(testEnv)
	wantNoErr(t, err)
	if res.Name != "shop" {
		t.Errorf("result = %+v", res)
	}
	if got := readFile(t, e.home("site/shared/.env")); got != testEnv {
		t.Errorf(".env = %q, want %q", got, testEnv)
	}
	checkModeOf(t, e.home("site/shared/.env"), 0o600)
	if !slices.Contains(e.h.ops, "write site/shared/.env 600") {
		t.Errorf("ops = %v, want the helper's write of site/shared/.env, mode 600", e.h.ops)
	}
	for _, op := range e.h.ops {
		if strings.HasPrefix(op, "exec ") || strings.Contains(op, "dummy") {
			t.Errorf("unexpected op %q", op)
		}
	}
	if len(e.h.reqs) != 0 {
		t.Errorf("execs = %v, want none", e.h.reqs)
	}
	e.noSecretIn(nil)
}

// A site without site/shared gets it, 0750 as provisioning makes it for a
// laravel site; the next deploy links the .env into its release.
func TestEnvWriteCreatesShared(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	_, err := e.writeEnv(testEnv)
	wantNoErr(t, err)
	checkModeOf(t, e.home("site/shared"), 0o750|os.ModeDir)
	checkModeOf(t, e.home("site/shared/.env"), 0o600)
	if !slices.Contains(e.h.ops, "mkdir site/shared 750") {
		t.Errorf("ops = %v, want site/shared made through the helper", e.h.ops)
	}
}

// site/shared that isn't a directory (a planted link) is refused; nothing
// is written through it.
func TestEnvWriteRefusesSharedLink(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, e.home("site/shared")); err != nil {
		t.Fatal(err)
	}
	_, err := e.writeEnv(testEnv)
	wantAgentCodeOf(t, err, contract.ErrConflict)
	if _, err := os.Stat(elsewhere + "/.env"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a .env was written through the link: %v", err)
	}
	e.noSecretIn(err)
}

// A planted shared/.env link is replaced by the new file (the helper's
// rename), never written through.
func TestEnvWriteReplacesEnvLink(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	outside := t.TempDir() + "/target"
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(e.home("site/shared"), 0o750)
	if err := os.Symlink(outside, e.home("site/shared/.env")); err != nil {
		t.Fatal(err)
	}
	_, err := e.writeEnv(testEnv)
	wantNoErr(t, err)
	if got := readFile(t, outside); got != "untouched" {
		t.Errorf("the link's target = %q, want it untouched", got)
	}
	checkModeOf(t, e.home("site/shared/.env"), 0o600)
}

func TestEnvWriteRefusesNUL(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	e.writeShared("OLD=1\n", 0o600)
	for _, content := range []string{
		"A=dummy-secret\x00\n",
		"A=dummy-secret\xff\n",
		strings.Repeat("x", contract.MaxTextFileBytes+1),
	} {
		_, err := e.writeEnv(content)
		ae := wantAgentCodeOf(t, err, contract.ErrValidationFailed)
		if ae.Fields["content"] == "" {
			t.Errorf("fields = %v, want content", ae.Fields)
		}
		e.noSecretIn(err)
	}
	if len(e.h.ops) != 0 {
		t.Errorf("ops = %v, want none for refused content", e.h.ops)
	}
	if got := readFile(t, e.home("site/shared/.env")); got != "OLD=1\n" {
		t.Errorf(".env = %q, want it unchanged", got)
	}
}

func TestEnvWriteUnknownSiteIsNotFound(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	_, err := WriteEnv(context.Background(), e.d, e.h, e.lookup, contract.SiteEnvWriteParams{Name: "nosuch", Content: testEnv})
	wantAgentCodeOf(t, err, contract.ErrNotFound)
	wantAgentCodeOf(t, CheckEnvWrite(e.d, contract.SiteEnvWriteParams{Name: "nosuch", Content: testEnv}), contract.ErrNotFound)
	wantNoErr(t, CheckEnvWrite(e.d, contract.SiteEnvWriteParams{Name: "shop", Content: testEnv}))
}

func wantAgentCodeOf(t *testing.T, err error, code contract.ErrorCode) *contract.AgentError {
	t.Helper()
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return ae
}
