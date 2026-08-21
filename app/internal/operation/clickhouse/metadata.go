package clickhouse

import "setpoint/internal/operation"

func OperationMetadata() operation.Metadata {
	return operation.Metadata{
		ID:               OperationID,
		Category:         "data_migration",
		Name:             "ClickHouse online migration",
		Version:          "0.2.0",
		Description:      "Discover, validate and migrate selected ClickHouse datasets through staged verified execution.",
		Risk:             operation.RiskHigh,
		Impact:           "Writes only through a verified Controlled Operation plan; the bounded Apply slice requires single-node Atomic MergeTree targets and a verified run-owned restore point.",
		SupportedSystems: []string{"linux"},
		Parameters: []operation.Parameter{
			{Name: "source", Type: "object", Description: "Source ClickHouse endpoint", Required: true, Fields: endpointParameterFields()},
			{Name: "target", Type: "object", Description: "Target ClickHouse endpoint", Required: true, Fields: endpointParameterFields()},
			{Name: "database", Type: "string", Description: "Database to migrate", Required: true},
			{Name: "tables", Type: "string[]", Description: "Selected tables", Required: true},
			{Name: "time_column", Type: "string", Description: "Optional event-time column for a bounded migration"},
			{Name: "start_time", Type: "string", Description: "Optional RFC3339 range start"},
			{Name: "end_time", Type: "string", Description: "Optional RFC3339 range end"},
		},
		SecretRequirements: []operation.SecretRequirement{
			{ID: "clickhouse_source_credential", Description: "Optional source ClickHouse credential referenced at runtime only"},
			{ID: "clickhouse_target_credential", Description: "Optional target ClickHouse credential referenced at runtime only"},
		},
	}
}

func endpointParameterFields() []operation.ParameterField {
	return []operation.ParameterField{
		{Name: "host", Type: "string", Description: "Host name or address", Required: true},
		{Name: "port", Type: "integer", Description: "Native protocol port; defaults to 9000 or 9440"},
		{Name: "user", Type: "string", Description: "Database user name"},
		{Name: "secure", Type: "boolean", Description: "Use the secure native protocol"},
	}
}
