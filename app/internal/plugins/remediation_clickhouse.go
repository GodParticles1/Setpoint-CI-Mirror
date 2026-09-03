package plugins

import "setpoint/internal/plugin"

const (
	clickhouseControlledReason = "The finding can feed an existing bounded administrative or migration workflow, but changing service, permissions, capacity, replica state, or migration scope requires controlled approval and verification."
	clickhouseFactReason       = "This Check is a prerequisite or inventory fact rather than a remediation target; remediation is defined by the consuming workflow, not by this finding itself."
)

var clickhouseRemediation = map[string]plugin.RemediationMetadata{
	"clickhouse.component.present":            remediation(plugin.RemediationNotApplicable, clickhouseFactReason),
	"clickhouse.version.detected":             remediation(plugin.RemediationNotApplicable, clickhouseFactReason),
	"clickhouse.runtime.available":            remediation(plugin.RemediationControlled, clickhouseControlledReason),
	"clickhouse.endpoint.query_reachable":     remediation(plugin.RemediationControlled, clickhouseControlledReason),
	"clickhouse.server.readonly_health":       remediation(plugin.RemediationControlled, clickhouseControlledReason),
	"clickhouse.disk.capacity_evidence":       remediation(plugin.RemediationControlled, clickhouseControlledReason),
	"clickhouse.catalog.databases":            remediation(plugin.RemediationNotApplicable, clickhouseFactReason),
	"clickhouse.catalog.tables":               remediation(plugin.RemediationNotApplicable, clickhouseFactReason),
	"clickhouse.replication.state":            remediation(plugin.RemediationControlled, clickhouseControlledReason),
	"clickhouse.cluster.topology":             remediation(plugin.RemediationNotApplicable, clickhouseFactReason),
	"clickhouse.migration.prerequisites":      remediation(plugin.RemediationControlled, clickhouseControlledReason),
	"clickhouse.atomic_exchange.capability":   remediation(plugin.RemediationControlled, clickhouseControlledReason),
	"clickhouse.migration.pair_compatibility": remediation(plugin.RemediationControlled, clickhouseControlledReason),
}
