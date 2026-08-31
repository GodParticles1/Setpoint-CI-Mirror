package clickhouse

import "setpoint/internal/operation"

func OperationMetadata() operation.Metadata {
	return operation.Metadata{
		ID:               OperationID,
		Category:         "数据迁移",
		Name:             "ClickHouse 在线迁移",
		Version:          "0.2.0",
		Description:      "发现并验证源端与目标端，通过经过校验的暂存和 Atomic EXCHANGE 安全迁移选定的 ClickHouse 数据。",
		Risk:             operation.RiskHigh,
		Impact:           "仅按已确认的受控操作计划写入；有界 Apply 要求单节点 Atomic MergeTree 目标、Atomic EXCHANGE 能力和已验证的本次运行恢复点。",
		SupportedSystems: []string{"linux"},
		Parameters: []operation.Parameter{
			{Name: "source", Type: "object", Description: "源 ClickHouse", Required: true, Fields: endpointParameterFields()},
			{Name: "target", Type: "object", Description: "目标 ClickHouse", Required: true, Fields: endpointParameterFields()},
			{Name: "database", Type: "string", Description: "待迁移数据库", Required: true},
			{Name: "tables", Type: "string[]", Description: "待迁移表", Required: true},
			{Name: "time_column", Type: "string", Description: "事件时间列（可选）"},
			{Name: "start_time", Type: "string", Description: "时间范围开始（可选，RFC3339）"},
			{Name: "end_time", Type: "string", Description: "时间范围结束（可选，RFC3339）"},
		},
		SecretRequirements: []operation.SecretRequirement{
			{ID: "clickhouse_source_credential", Description: "源 ClickHouse 运行时凭据引用（可选）"},
			{ID: "clickhouse_target_credential", Description: "目标 ClickHouse 运行时凭据引用（可选）"},
		},
	}
}

func endpointParameterFields() []operation.ParameterField {
	return []operation.ParameterField{
		{Name: "host", Type: "string", Description: "主机名或地址", Required: true},
		{Name: "port", Type: "integer", Description: "Native 协议端口（常用 9000 / 9440）"},
		{Name: "user", Type: "string", Description: "数据库用户名"},
		{Name: "secure", Type: "boolean", Description: "使用安全 Native 协议"},
	}
}
