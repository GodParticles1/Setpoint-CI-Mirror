//go:build !linux

package xrocketreaddress

import (
	"errors"

	"setpoint/internal/executor"
)

func newProductionRecoveryArtifactCollector(executor.CommandExecutor) (RecoveryArtifactCollector, error) {
	return nil, errors.New("xRocket production recovery capture is supported only on Linux")
}

func newProductionReadOnlyInspector(executor.CommandExecutor) (ProductionReadOnlyInspector, error) {
	return nil, errors.New("xRocket production recovery inspection is supported only on Linux")
}
