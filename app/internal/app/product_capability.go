package app

import (
	"errors"
	"strings"
)

const (
	OperationExecutionUnavailableBlock = "operation_execution_unavailable"
	SecretDeliveryUnavailableBlock     = "secret_delivery_unavailable"
)

var (
	ErrOperationExecutionUnavailable = errors.New(OperationExecutionUnavailableBlock)
	ErrSecretDeliveryUnavailable     = errors.New(SecretDeliveryUnavailableBlock)
)

type ProductExecutionCapability struct {
	OperationID    string
	ApplyAvailable bool
	BlockCode      string
}

type ProductExecutionResolver struct {
	capabilities map[string]ProductExecutionCapability
}

func NewProductExecutionResolver(capabilities ...ProductExecutionCapability) (*ProductExecutionResolver, error) {
	resolver := &ProductExecutionResolver{capabilities: make(map[string]ProductExecutionCapability, len(capabilities))}
	for _, capability := range capabilities {
		capability.OperationID = strings.TrimSpace(capability.OperationID)
		capability.BlockCode = strings.TrimSpace(capability.BlockCode)
		if capability.OperationID == "" {
			return nil, errors.New("product execution capability operation ID is required")
		}
		if _, duplicate := resolver.capabilities[capability.OperationID]; duplicate {
			return nil, errors.New("duplicate product execution capability " + capability.OperationID)
		}
		if !capability.ApplyAvailable && capability.BlockCode == "" {
			capability.BlockCode = OperationExecutionUnavailableBlock
		}
		if capability.ApplyAvailable {
			capability.BlockCode = ""
		}
		resolver.capabilities[capability.OperationID] = capability
	}
	return resolver, nil
}

func (resolver *ProductExecutionResolver) Resolve(operationID string) (ProductExecutionCapability, bool) {
	if resolver == nil {
		return ProductExecutionCapability{}, false
	}
	capability, ok := resolver.capabilities[strings.TrimSpace(operationID)]
	return capability, ok
}
