package sysctlrepair

import (
	"errors"

	"setpoint/internal/operation"
	"setpoint/internal/operation/clickhouse"
)

// DefinitionFactory adapts the first Product V1 repair capability to the
// already-frozen bounded-action runner. The ClickHouse ledger argument belongs
// to the carried-forward runner contract and is intentionally unused here.
type DefinitionFactory struct {
	definition *Definition
}

func NewDefinitionFactory(definition *Definition) (*DefinitionFactory, error) {
	if definition == nil {
		return nil, errors.New("sysctl repair definition is required")
	}
	return &DefinitionFactory{definition: definition}, nil
}

func (factory *DefinitionFactory) Definition(clickhouse.LedgerStore) (operation.OperationDefinition, error) {
	if factory == nil || factory.definition == nil {
		return nil, errors.New("sysctl repair definition is unavailable")
	}
	return factory.definition, nil
}
