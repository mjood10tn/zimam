package contract

import "testing"

// Every §5.3 command is registered. With TestDescriptorsMatchDesign, which
// checks each registered command against its §5.3 row and refuses one that
// isn't there, this proves the list is complete.
func TestAllCommandsRegistered(t *testing.T) {
	if len(designCommands) != 18 {
		t.Fatalf("§5.3 lists %d commands here, want 18", len(designCommands))
	}
	for _, row := range designCommands {
		if _, ok := Lookup(row.name); !ok {
			t.Errorf("%s isn't registered", row.name)
		}
	}
}

func TestPolicyBounds(t *testing.T) {
	for p, ok := range map[LogsPolicyParams]bool{
		{RetentionDays: 14, AuditRetentionDays: 365, SiteCapBytes: SiteCapBytes(5 * GiB)}:     true,
		{RetentionDays: 1, AuditRetentionDays: 1, SiteCapBytes: SiteCapBytes(GiB)}:            true,
		{RetentionDays: 365, AuditRetentionDays: 3650, SiteCapBytes: SiteCapBytes(100 * GiB)}: true,
		{RetentionDays: 0, AuditRetentionDays: 365, SiteCapBytes: SiteCapBytes(5 * GiB)}:      false,
		{RetentionDays: 366, AuditRetentionDays: 365, SiteCapBytes: SiteCapBytes(5 * GiB)}:    false,
		{RetentionDays: 14, AuditRetentionDays: 3651, SiteCapBytes: SiteCapBytes(5 * GiB)}:    false,
		{RetentionDays: 14, AuditRetentionDays: 365, SiteCapBytes: SiteCapBytes(100*GiB + 1)}: false,
	} {
		if err := p.Validate(); (err == nil) != ok {
			t.Errorf("%+v: Validate = %v", p, err)
		}
	}
}
