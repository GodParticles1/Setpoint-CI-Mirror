package xrocketreaddress

import (
	"errors"

	"setpoint/internal/executor"
	"setpoint/internal/operation"
)

// ProductionReadOnlyInspector combines the accepted forward and rollback
// read-only inspection contracts. It intentionally exposes no mutation method.
type ProductionReadOnlyInspector interface {
	LocalInspectionAdapter
	LocalRollbackInspectionAdapter
}

// NewProductionRecoveryArtifactCollector builds the Linux production recovery
// collector. Unsupported operating systems fail closed in the platform-specific
// constructor.
func NewProductionRecoveryArtifactCollector(commandExecutor executor.CommandExecutor) (RecoveryArtifactCollector, error) {
	if commandExecutor == nil {
		return nil, errors.New("xRocket production recovery collector requires a command executor")
	}
	return newProductionRecoveryArtifactCollector(commandExecutor)
}

// NewProductionReadOnlyInspector builds the production observation layer used
// by direct/package tests and a later, separately reviewed composition slice.
func NewProductionReadOnlyInspector(commandExecutor executor.CommandExecutor) (ProductionReadOnlyInspector, error) {
	if commandExecutor == nil {
		return nil, errors.New("xRocket production read-only inspector requires a command executor")
	}
	return newProductionReadOnlyInspector(commandExecutor)
}

// NewProductionRestorePointProvider composes only recovery capture and
// read-only rollback inspection. It deliberately does not construct a
// Definition and cannot make Apply/Rollback mutation available.
func NewProductionRestorePointProvider(commandExecutor executor.CommandExecutor) (operation.RestorePointProvider, error) {
	collector, err := NewProductionRecoveryArtifactCollector(commandExecutor)
	if err != nil {
		return nil, err
	}
	inspector, err := NewProductionReadOnlyInspector(commandExecutor)
	if err != nil {
		return nil, err
	}
	return newRestorePointProviderWithRecovery(commandExecutor, collector, inspector)
}
