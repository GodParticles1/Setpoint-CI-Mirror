package checkutil

import (
	"errors"

	"setpoint/internal/executor"
)

func ErrorCode(err error, fallback string) string {
	var executionError *executor.Error
	if errors.As(err, &executionError) {
		return string(executionError.Kind)
	}
	return fallback
}
