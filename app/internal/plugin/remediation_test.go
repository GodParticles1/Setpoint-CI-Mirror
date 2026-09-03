package plugin

import "testing"

func TestValidateRemediationMetadata(t *testing.T) {
	tests := []struct {
		name    string
		value   RemediationMetadata
		wantErr bool
	}{
		{name: "auto_safe", value: RemediationMetadata{Disposition: RemediationAutoSafe, OperationID: "linux.network.icmp_redirects.runtime_repair", Reason: "bounded operation"}},
		{name: "controlled", value: RemediationMetadata{Disposition: RemediationControlled, Reason: "approval required"}},
		{name: "manual_only", value: RemediationMetadata{Disposition: RemediationManualOnly, Reason: "ambiguous source"}},
		{name: "not_applicable", value: RemediationMetadata{Disposition: RemediationNotApplicable, Reason: "observation only"}},
		{name: "missing_disposition", value: RemediationMetadata{Reason: "missing state"}, wantErr: true},
		{name: "missing_reason", value: RemediationMetadata{Disposition: RemediationControlled}, wantErr: true},
		{name: "auto_missing_operation", value: RemediationMetadata{Disposition: RemediationAutoSafe, Reason: "missing operation"}, wantErr: true},
		{name: "manual_with_operation", value: RemediationMetadata{Disposition: RemediationManualOnly, OperationID: "unexpected.operation", Reason: "must not bind"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRemediationMetadata(test.value)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateRemediationMetadata(%#v) error=%v wantErr=%v", test.value, err, test.wantErr)
			}
		})
	}
}
