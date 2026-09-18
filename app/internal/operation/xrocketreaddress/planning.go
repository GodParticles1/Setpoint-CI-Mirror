package xrocketreaddress

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"setpoint/internal/operation"
)

const (
	precheckSchema = "xrocket.readdress.precheck.v1"
	planSchema     = "xrocket.readdress.plan.v1"
)

type precheckState struct {
	SchemaVersion string         `json:"schema_version"`
	Parameters    parameters     `json:"parameters"`
	Discovery     discoveryState `json:"discovery"`
	MasterProfile runtimeProfile `json:"master_profile"`
	MasterNodeID  string         `json:"master_node_id"`
	SlaveNodeID   string         `json:"slave_node_id"`
}

type executionPlan struct {
	SchemaVersion string         `json:"schema_version"`
	Parameters    parameters     `json:"parameters"`
	Discovery     discoveryState `json:"discovery"`
	MasterProfile runtimeProfile `json:"master_profile"`
	MasterNodeID  string         `json:"master_node_id"`
	SlaveNodeID   string         `json:"slave_node_id"`
}

func nodeParticipants(targets []operation.Target, localNodeID string) (string, string, error) {
	var ids []string
	seen := map[string]struct{}{}
	for _, target := range targets {
		if target.Kind != operation.TargetNode || strings.TrimSpace(target.NodeID) == "" {
			continue
		}
		if _, duplicate := seen[target.NodeID]; duplicate {
			continue
		}
		seen[target.NodeID] = struct{}{}
		ids = append(ids, target.NodeID)
	}
	if len(ids) != 2 {
		return "", "", fmt.Errorf("xRocket readdress requires exactly two node participants, found %d", len(ids))
	}
	if _, ok := seen[localNodeID]; !ok {
		return "", "", errors.New("planning Agent is outside the two-node participant set")
	}
	for _, id := range ids {
		if id != localNodeID {
			return localNodeID, id, nil
		}
	}
	return "", "", errors.New("peer participant is missing")
}

func validateBoundedPrecheck(value parameters, discovery discoveryState, profile runtimeProfile) error {
	if !discovery.resolved() || discovery.RuntimeRole != "active_vip_owner" {
		return errors.New("planning Agent must positively prove current VIP ownership")
	}
	if !discovery.KeepalivedNopreempt || discovery.KeepalivedPriority == 0 || len(discovery.BusinessPorts) == 0 {
		return errors.New("bounded HA contract requires nopreempt, priority and business-port evidence")
	}
	if !strings.Contains(discovery.ProductGeneration, "R004C68") {
		return fmt.Errorf("unsupported xRocket generation %q; bounded execution requires R4/C68", discovery.ProductGeneration)
	}
	if profile.OSAuthorityUID != 0 || profile.ServiceControlAdapter != "monit" || profile.NetworkBackend != "ifcfg" {
		return errors.New("bounded C68 execution requires root OS authority, Monit control and ifcfg network backend")
	}
	if profile.EtcdMemberCount != 1 || profile.EtcdMemberID == "" || profile.EtcdClientPort == 0 || profile.EtcdPeerPort == 0 {
		return errors.New("bounded C68 execution requires one independently discovered singleton etcd member")
	}
	if profile.PersistentAddress != discovery.MasterAddress || profile.PersistentPrefix != discovery.PrefixLength || profile.PersistentGateway != discovery.GatewayAddress {
		return errors.New("persistent network truth differs from runtime discovery")
	}
	if err := validateSiteSubnet(discovery, value); err != nil {
		return err
	}
	oldSite := map[string]struct{}{discovery.MasterAddress: {}, discovery.SlaveAddress: {}, discovery.VIPAddress: {}}
	newSite := []string{value.MasterTargetAddress, value.SlaveTargetAddress, value.VIPTargetAddress}
	for _, address := range newSite {
		if _, collision := oldSite[address]; collision {
			return fmt.Errorf("target site address %s overlaps the current site", address)
		}
		if address == profile.ExternalDBAddress {
			return fmt.Errorf("target site address %s overlaps the current external DB target", address)
		}
	}
	if value.ExternalDBTargetAddress != profile.ExternalDBAddress {
		if _, collision := oldSite[value.ExternalDBTargetAddress]; collision {
			return errors.New("new external DB target overlaps the current site")
		}
		for _, address := range newSite {
			if value.ExternalDBTargetAddress == address {
				return errors.New("new external DB target overlaps the target site")
			}
		}
	}
	return nil
}

func validateSiteSubnet(discovery discoveryState, value parameters) error {
	mask := net.CIDRMask(discovery.PrefixLength, 32)
	if mask == nil {
		return errors.New("discovered prefix is invalid")
	}
	anchor := net.ParseIP(discovery.MasterAddress).To4()
	if anchor == nil {
		return errors.New("discovered Master address is invalid")
	}
	network := &net.IPNet{IP: anchor.Mask(mask), Mask: mask}
	for name, raw := range map[string]string{
		"current slave": discovery.SlaveAddress,
		"current vip":   discovery.VIPAddress,
		"gateway":       discovery.GatewayAddress,
		"target master": value.MasterTargetAddress,
		"target slave":  value.SlaveTargetAddress,
		"target vip":    value.VIPTargetAddress,
	} {
		ip := net.ParseIP(raw)
		if ip == nil || !network.Contains(ip) {
			return fmt.Errorf("%s address %s is outside the discovered /%d network", name, raw, discovery.PrefixLength)
		}
	}
	return nil
}

func decodePrecheck(artifact operation.Artifact) (precheckState, error) {
	if artifact.SchemaVersion != precheckSchema {
		return precheckState{}, fmt.Errorf("unsupported xRocket precheck schema %q", artifact.SchemaVersion)
	}
	var state precheckState
	if err := json.Unmarshal(artifact.Payload, &state); err != nil {
		return precheckState{}, fmt.Errorf("decode xRocket precheck: %w", err)
	}
	if state.SchemaVersion != precheckSchema || state.MasterNodeID == "" || state.SlaveNodeID == "" {
		return precheckState{}, errors.New("xRocket precheck correlation is invalid")
	}
	return state, nil
}

func decodeExecutionPlan(plan operation.Plan) (executionPlan, error) {
	if plan.SchemaVersion != planSchema || plan.Execution.SchemaVersion != planSchema {
		return executionPlan{}, errors.New("xRocket execution plan schema is invalid")
	}
	var state executionPlan
	if err := json.Unmarshal(plan.Execution.Payload, &state); err != nil {
		return executionPlan{}, fmt.Errorf("decode xRocket execution plan: %w", err)
	}
	if state.SchemaVersion != planSchema || state.MasterNodeID == "" || state.SlaveNodeID == "" {
		return executionPlan{}, errors.New("xRocket execution plan correlation is invalid")
	}
	return state, nil
}

func buildPlan(state precheckState) (operation.Plan, error) {
	masterTarget := operation.Target{Kind: operation.TargetNode, NodeID: state.MasterNodeID}
	slaveTarget := operation.Target{Kind: operation.TargetNode, NodeID: state.SlaveNodeID}
	step := func(id, name string, target operation.Target, action, checkpoint string, writes, retrySafe bool, rollback string, barrier operation.StageBarrier) operation.PlanStep {
		return operation.PlanStep{ID: id, Name: name, Target: target, Action: action, Checkpoint: checkpoint, Writes: writes, RetrySafe: retrySafe, RollbackAction: rollback, ExecutorNodeID: target.NodeID, Barrier: barrier}
	}
	steps := []operation.PlanStep{
		step("snapshot-slave", "Freeze Slave RestorePoint", slaveTarget, "snapshot_baseline", "slave_restorepoint_verified", false, true, "", ""),
		step("snapshot-master", "Freeze Master RestorePoint", masterTarget, "snapshot_baseline", "master_restorepoint_verified", false, true, "", ""),
		step("alias-slave", "Add Slave target address alias", slaveTarget, "add_target_alias", "slave_alias_verified", true, false, "restore_baseline", ""),
		step("alias-master", "Add Master target address alias", masterTarget, "add_target_alias", "master_alias_verified", true, false, "restore_baseline", ""),
		step("product-slave", "Readdress xRocket product state on Slave", slaveTarget, "product_update_ip", "slave_product_readdressed", true, false, "restore_baseline", ""),
		step("product-master", "Readdress xRocket product state on Master", masterTarget, "product_update_ip", "master_product_readdressed", true, false, "restore_baseline", ""),
		step("external-db-slave", "Update Slave external DB target IP", slaveTarget, "external_db_ip", "slave_external_db_updated", true, false, "restore_baseline", ""),
		step("external-db-master", "Update Master external DB target IP", masterTarget, "external_db_ip", "master_external_db_updated", true, false, "restore_baseline", ""),
		step("etcd-slave", "Readdress Slave singleton etcd", slaveTarget, "etcd_readdress", "slave_etcd_healthy", true, false, "restore_baseline", ""),
		step("etcd-master", "Readdress Master singleton etcd", masterTarget, "etcd_readdress", "master_etcd_healthy", true, false, "restore_baseline", ""),
		step("confd-slave", "Render and verify Slave confd outputs", slaveTarget, "confd_render", "slave_confd_verified", true, false, "restore_baseline", ""),
		step("confd-master", "Render and verify Master confd outputs", masterTarget, "confd_render", "master_confd_verified", true, false, "restore_baseline", ""),
		step("os-slave", "Persist Slave OS address and reboot", slaveTarget, "os_cutover_reboot", "slave_reconnected_verified", true, false, "restore_baseline", operation.StageBarrierAgentReconnect),
		step("os-master", "Persist Master OS address and reboot", masterTarget, "os_cutover_reboot", "master_reconnected_verified", true, false, "restore_baseline", operation.StageBarrierAgentReconnect),
		step("final-slave", "Verify nopreempt VIP holder and business endpoints", slaveTarget, "final_site_verify", "site_readdress_verified", false, true, "", ""),
	}
	execution := executionPlan{
		SchemaVersion: planSchema,
		Parameters:    state.Parameters, Discovery: state.Discovery, MasterProfile: state.MasterProfile,
		MasterNodeID: state.MasterNodeID, SlaveNodeID: state.SlaveNodeID,
	}
	artifact, err := encodeArtifact(planSchema, execution)
	if err != nil {
		return operation.Plan{}, err
	}
	return operation.Plan{
		SchemaVersion: planSchema,
		Summary:       "Freeze both RestorePoints before mutation, readdress product/DB/etcd/confd, then cut over Slave before Master with durable Agent reconnect barriers",
		Steps:         steps,
		Execution:     artifact,
	}, nil
}

func buildImpact(plan executionPlan) operation.Impact {
	master := operation.Target{Kind: operation.TargetNode, NodeID: plan.MasterNodeID}
	slave := operation.Target{Kind: operation.TargetNode, NodeID: plan.SlaveNodeID}
	changes := []operation.Change{
		{Target: master, Before: plan.Discovery.MasterAddress, After: plan.Parameters.MasterTargetAddress, Risk: "critical: product, etcd and persistent OS identity"},
		{Target: slave, Before: plan.Discovery.SlaveAddress, After: plan.Parameters.SlaveTargetAddress, Risk: "critical: product, etcd and persistent OS identity"},
		{Target: master, Before: plan.Discovery.VIPAddress, After: plan.Parameters.VIPTargetAddress, Risk: "critical: HA/VIP business entry"},
		{Target: master, Before: plan.MasterProfile.ExternalDBAddress, After: plan.Parameters.ExternalDBTargetAddress, Risk: "high: xRocket external DB target IP only; existing access port and credentials preserved"},
	}
	return operation.Impact{
		Summary:            "Two-node xRocket C68/R4 site readdress with staged reboots and RestorePoint-driven recovery",
		Risk:               operation.RiskCritical,
		Changes:            changes,
		RequiresDowntime:   true,
		RequiresWriteFence: false,
		EstimatedDuration:  20 * time.Minute,
	}
}
