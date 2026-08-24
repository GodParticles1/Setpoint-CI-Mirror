package sysctlrepair

import (
	"encoding/json"

	"setpoint/internal/operation"
)

type CatalogDescriptor struct{}

func NewCatalogDescriptor() CatalogDescriptor { return CatalogDescriptor{} }

func (CatalogDescriptor) Metadata() operation.Metadata { return Metadata() }

func (CatalogDescriptor) NormalizeParameters(raw json.RawMessage) (json.RawMessage, error) {
	value, err := decodeParameters(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
