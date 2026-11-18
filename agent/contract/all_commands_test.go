package contract

import "testing"

// Every §5.3 command, and every command the v0.2.0 plan adds, is registered.
// With TestDescriptorsMatchDesign, which checks each registered command
// against its row and refuses one that isn't there, this proves the list is
// complete.
func TestAllCommandsRegistered(t *testing.T) {
	if len(designCommands) != 37 {
		t.Fatalf("the table lists %d commands, want 37 (§5.3's 18, v0.2.0's 6, v0.3.0's 7 and v0.4.0's 6)", len(designCommands))
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
