package api

import (
	"testing"

	"setpoint/internal/app"
)

func TestOperationCapabilityConflictCodesRemainTyped(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{err: &app.ConflictError{Err: app.ErrOperationExecutionUnavailable}, code: app.OperationExecutionUnavailableBlock},
		{err: &app.ConflictError{Err: app.ErrSecretDeliveryUnavailable}, code: app.SecretDeliveryUnavailableBlock},
	}
	for _, test := range tests {
		if got := serviceConflictCode(test.err); got != test.code {
			t.Fatalf("error=%v code=%q want=%q", test.err, got, test.code)
		}
	}
}
