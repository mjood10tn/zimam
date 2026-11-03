package contract

import (
	"errors"
	"testing"
)

func provisionFields(t *testing.T, p SiteProvisionParams) FieldErrors {
	t.Helper()
	err := p.Validate()
	if err == nil {
		return nil
	}
	var ae *AgentError
	if !errors.As(err, &ae) || ae.Code != ErrValidationFailed {
		t.Fatalf("Validate = %v, want validation_failed", err)
	}
	return ae.Fields
}

func TestStaticRejectsPHPVersion(t *testing.T) {
	ok := SiteProvisionParams{Name: "shop", Domain: "shop.example.com", Type: SiteStatic}
	if f := provisionFields(t, ok); f != nil {
		t.Errorf("a static site without a PHP version: %v", f)
	}
	ok.PHPVersion = "8.4"
	if f := provisionFields(t, ok); f["php_version"] == "" {
		t.Errorf("fields = %v, want php_version refused for a static site", f)
	}
}

func TestPHPRequiresVersion(t *testing.T) {
	p := SiteProvisionParams{Name: "shop", Domain: "shop.example.com", Type: SitePHP}
	if f := provisionFields(t, p); f["php_version"] == "" {
		t.Errorf("fields = %v, want php_version required for a PHP site", f)
	}
	p.PHPVersion = "7.4"
	if f := provisionFields(t, p); f["php_version"] == "" {
		t.Errorf("fields = %v, want 7.4 refused", f)
	}
	p.PHPVersion = "8.4"
	if f := provisionFields(t, p); f != nil {
		t.Errorf("a PHP 8.4 site: %v", f)
	}
}

func TestProvisionParamsCheckEveryField(t *testing.T) {
	f := provisionFields(t, SiteProvisionParams{Name: "Shop", Domain: "shop", Type: "wordpress"})
	for _, field := range []string{"name", "domain", "type"} {
		if f[field] == "" {
			t.Errorf("fields = %v, want %s refused", f, field)
		}
	}
}
