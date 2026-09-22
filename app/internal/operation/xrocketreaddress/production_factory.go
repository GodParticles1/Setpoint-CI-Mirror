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

// productionLocalAdapters combines only the frozen Agent-local forward and
// rollback mutation contracts. It is intentionally package-local in this slice.
type productionLocalAdapters interface {
	LocalMutationAdapter
	LocalRollbackMutationAdapter
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

// newProductionDefinitionWithLocalAdapters exists only to prove package-local
// composition. No Agent resolver calls this constructor in this checkpoint.
func newProductionDefinitionWithLocalAdapters(commandExecutor executor.CommandExecutor) (*Definition, error) {
	mutator, err := newProductionMutationAdapter(commandExecutor)
	if err != nil {
		return nil, err
	}
	inspector, err := NewProductionReadOnlyInspector(commandExecutor)
	if err != nil {
		return nil, err
	}
	return NewDefinitionWithStageAdapters(commandExecutor, mutator, inspector)
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
