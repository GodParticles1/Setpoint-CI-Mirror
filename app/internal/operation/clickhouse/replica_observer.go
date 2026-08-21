package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type ReplicaPartitionState string

const (
	ReplicaPartitionConverged ReplicaPartitionState = "converged"
	ReplicaPartitionPending   ReplicaPartitionState = "pending"
	ReplicaPartitionConflict  ReplicaPartitionState = "conflict"
)

type ReplicaPartitionExpectation string

const (
	ReplicaExpectSource ReplicaPartitionExpectation = "source"
	ReplicaExpectAbsent ReplicaPartitionExpectation = "absent"
)

type ReplicaPartitionObservation struct {
	Endpoint      Endpoint        `json:"endpoint"`
	Cluster       string          `json:"cluster"`
	ShardNum      uint64          `json:"shard_num"`
	ReplicaNum    uint64          `json:"replica_num"`
	ReplicaName   string          `json:"replica_name,omitempty"`
	Fingerprint   DataFingerprint `json:"fingerprint"`
	ActiveParts   uint64          `json:"active_parts"`
	QueueSize     uint64          `json:"queue_size"`
	FutureParts   uint64          `json:"future_parts"`
	PartsToCheck  uint64          `json:"parts_to_check"`
	LogLag        uint64          `json:"log_lag"`
	AbsoluteDelay uint64          `json:"absolute_delay"`
	Healthy       bool            `json:"healthy"`
	Matched       bool            `json:"matched"`
	Absent        bool            `json:"absent"`
	Conflicting   bool            `json:"conflicting"`
	Reason        string          `json:"reason,omitempty"`
}

type ReplicaPartitionReport struct {
	State       ReplicaPartitionState         `json:"state"`
	Expected    int                           `json:"expected"`
	Matched     int                           `json:"matched"`
	Absent      int                           `json:"absent"`
	Pending     int                           `json:"pending"`
	Conflicting int                           `json:"conflicting"`
	Replicas    []ReplicaPartitionObservation `json:"replicas"`
}

type replicaTarget struct {
	member   ClusterMember
	endpoint Endpoint
}

type ReplicaPartitionObserver struct {
	client   QueryClient
	verifier PartitionFingerprintVerifier
}

func NewReplicaPartitionObserver(client QueryClient, verifier FingerprintVerifier) (*ReplicaPartitionObserver, error) {
	if client == nil || verifier == nil {
		return nil, errors.New("ClickHouse query client and fingerprint verifier are required")
	}
	partitionVerifier, ok := verifier.(PartitionFingerprintVerifier)
	if !ok {
		return nil, errors.New("replica partition observation requires a partition-aware fingerprint verifier")
	}
	return &ReplicaPartitionObserver{client: client, verifier: partitionVerifier}, nil
}

func (observer *ReplicaPartitionObserver) ObserveSource(ctx context.Context, snapshot Snapshot, base Endpoint, database string, table Table, partition string, source DataFingerprint) (ReplicaPartitionReport, error) {
	return observer.observe(ctx, snapshot, base, database, table, partition, source, ReplicaExpectSource)
}

func (observer *ReplicaPartitionObserver) ObserveAbsent(ctx context.Context, snapshot Snapshot, base Endpoint, database string, table Table, partition string, source DataFingerprint) (ReplicaPartitionReport, error) {
	return observer.observe(ctx, snapshot, base, database, table, partition, source, ReplicaExpectAbsent)
}

func (observer *ReplicaPartitionObserver) observe(ctx context.Context, snapshot Snapshot, base Endpoint, database string, table Table, partition string, source DataFingerprint, expectation ReplicaPartitionExpectation) (ReplicaPartitionReport, error) {
	if !validIdentifier(database) || !validIdentifier(table.Name) {
		return ReplicaPartitionReport{}, errors.New("replica partition identifiers are invalid")
	}
	partition = strings.TrimSpace(partition)
	if partition == "" {
		return ReplicaPartitionReport{}, errors.New("replica partition observation requires a partition ID")
	}
	targets, err := expectedReplicaTargets(snapshot, base)
	if err != nil {
		return ReplicaPartitionReport{}, err
	}
	report := ReplicaPartitionReport{Expected: len(targets), Replicas: make([]ReplicaPartitionObservation, 0, len(targets))}
	for _, target := range targets {
		replica, err := observer.replicaHealth(ctx, target.endpoint, database, table.Name)
		if err != nil {
			return ReplicaPartitionReport{}, fmt.Errorf("observe replica %s health: %w", target.endpoint.Host, err)
		}
		parts, err := observer.activePartitionParts(ctx, target.endpoint, database, table.Name, partition)
		if err != nil {
			return ReplicaPartitionReport{}, fmt.Errorf("observe replica %s partition parts: %w", target.endpoint.Host, err)
		}
		fingerprint, err := observer.verifier.FingerprintPartition(ctx, target.endpoint, database, table, partition)
		if err != nil {
			return ReplicaPartitionReport{}, fmt.Errorf("observe replica %s partition fingerprint: %w", target.endpoint.Host, err)
		}
		observation := ReplicaPartitionObservation{
			Endpoint: target.endpoint, Cluster: target.member.Cluster, ShardNum: target.member.ShardNum, ReplicaNum: target.member.ReplicaNum,
			ReplicaName: replica.ReplicaName, Fingerprint: fingerprint, ActiveParts: parts, QueueSize: replica.QueueSize,
			FutureParts: replica.FutureParts, PartsToCheck: replica.PartsToCheck, LogLag: replica.LogLag, AbsoluteDelay: replica.AbsoluteDelay,
		}
		observation.Healthy = replicaHealthy(replica)
		observation.Absent = fingerprint.Rows == 0 && parts == 0
		sourceMatches := CompareFingerprints(source, fingerprint).Passed
		switch expectation {
		case ReplicaExpectSource:
			switch {
			case sourceMatches && observation.Healthy && parts > 0:
				observation.Matched = true
				report.Matched++
			case observation.Absent || sourceMatches:
				observation.Reason = "replica has not converged to a healthy source fingerprint"
				report.Pending++
			case fingerprint.Rows > 0:
				observation.Conflicting = true
				observation.Reason = "replica contains a non-run-owned partition fingerprint"
				report.Conflicting++
			default:
				observation.Reason = "replica convergence is pending"
				report.Pending++
			}
		case ReplicaExpectAbsent:
			switch {
			case observation.Absent && observation.Healthy:
				observation.Matched = true
				report.Matched++
				report.Absent++
			case observation.Absent || sourceMatches:
				observation.Reason = "replica partition removal is still converging"
				report.Pending++
			case fingerprint.Rows > 0:
				observation.Conflicting = true
				observation.Reason = "replica contains unexpected data while rollback is converging"
				report.Conflicting++
			default:
				observation.Reason = "replica rollback convergence is pending"
				report.Pending++
			}
		default:
			return ReplicaPartitionReport{}, fmt.Errorf("unsupported replica partition expectation %q", expectation)
		}
		if observation.Absent && expectation == ReplicaExpectSource {
			report.Absent++
		}
		report.Replicas = append(report.Replicas, observation)
	}
	if report.Conflicting > 0 {
		report.State = ReplicaPartitionConflict
	} else if report.Matched == report.Expected {
		report.State = ReplicaPartitionConverged
	} else {
		report.State = ReplicaPartitionPending
	}
	return report, nil
}

func expectedReplicaTargets(snapshot Snapshot, base Endpoint) ([]replicaTarget, error) {
	clusterNames := append([]string(nil), snapshot.Topology.ClusterNames...)
	if len(clusterNames) == 0 {
		seen := make(map[string]struct{})
		for _, member := range snapshot.Clusters {
			name := strings.TrimSpace(member.Cluster)
			if name != "" {
				seen[name] = struct{}{}
			}
		}
		for name := range seen {
			clusterNames = append(clusterNames, name)
		}
		sort.Strings(clusterNames)
	}
	if len(clusterNames) != 1 {
		return nil, fmt.Errorf("replicated partition lab requires exactly one discovered cluster, got %d", len(clusterNames))
	}
	cluster := clusterNames[0]
	shards := make(map[uint64]struct{})
	seenEndpoints := make(map[string]struct{})
	targets := make([]replicaTarget, 0)
	for _, member := range snapshot.Clusters {
		if member.Cluster != cluster {
			continue
		}
		if member.ShardNum == 0 || member.ReplicaNum == 0 {
			return nil, errors.New("replica discovery returned zero shard or replica number")
		}
		shards[member.ShardNum] = struct{}{}
		host := strings.TrimSpace(member.HostAddress)
		if host == "" {
			host = strings.TrimSpace(member.HostName)
		}
		if host == "" || member.Port == 0 || member.Port > 65535 {
			return nil, errors.New("replica discovery returned an invalid host or native port")
		}
		endpoint := Endpoint{Host: host, Port: uint16(member.Port), User: base.User, Secure: base.Secure}
		key := fmt.Sprintf("%s:%d", strings.ToLower(endpoint.Host), endpoint.Port)
		if _, exists := seenEndpoints[key]; exists {
			continue
		}
		seenEndpoints[key] = struct{}{}
		targets = append(targets, replicaTarget{member: member, endpoint: endpoint})
	}
	if len(shards) != 1 || snapshot.Topology.Shards > 1 {
		return nil, fmt.Errorf("replicated partition lab currently supports exactly one shard, got %d", len(shards))
	}
	if len(targets) < 2 {
		return nil, fmt.Errorf("replicated partition lab requires at least two replicas, got %d", len(targets))
	}
	if snapshot.Topology.Replicas > 0 && snapshot.Topology.Replicas != len(targets) {
		return nil, fmt.Errorf("discovered replica count mismatch: topology=%d endpoints=%d", snapshot.Topology.Replicas, len(targets))
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].member.ShardNum != targets[j].member.ShardNum {
			return targets[i].member.ShardNum < targets[j].member.ShardNum
		}
		if targets[i].member.ReplicaNum != targets[j].member.ReplicaNum {
			return targets[i].member.ReplicaNum < targets[j].member.ReplicaNum
		}
		return targets[i].endpoint.Host < targets[j].endpoint.Host
	})
	return targets, nil
}

func (observer *ReplicaPartitionObserver) replicaHealth(ctx context.Context, endpoint Endpoint, database, table string) (Replica, error) {
	query := fmt.Sprintf(`SELECT
    database,
    table,
    toString(is_leader) AS is_leader,
    toString(is_readonly) AS is_readonly,
    toString(is_session_expired) AS is_session_expired,
    toString(future_parts) AS future_parts,
    toString(parts_to_check) AS parts_to_check,
    toString(queue_size) AS queue_size,
    toString(inserts_in_queue) AS inserts_in_queue,
    toString(merges_in_queue) AS merges_in_queue,
    toString(if(log_max_index >= log_pointer, log_max_index - log_pointer, 0)) AS log_lag,
    toString(absolute_delay) AS absolute_delay,
    zookeeper_path,
    replica_name
FROM system.replicas
WHERE database = %s AND table = %s`, quoteLiteral(database), quoteLiteral(table))
	raw, err := observer.client.Query(ctx, queryForEndpoint(endpoint, database, query, FormatJSONEachRow))
	if err != nil {
		return Replica{}, err
	}
	replicas, err := parseReplicas(raw)
	if err != nil {
		return Replica{}, err
	}
	if len(replicas) != 1 {
		return Replica{}, fmt.Errorf("expected one local system.replicas row, got %d", len(replicas))
	}
	return replicas[0], nil
}

func (observer *ReplicaPartitionObserver) activePartitionParts(ctx context.Context, endpoint Endpoint, database, table, partition string) (uint64, error) {
	query := fmt.Sprintf("SELECT toString(count()) FROM system.parts WHERE active = 1 AND database = %s AND table = %s AND partition_id = %s", quoteLiteral(database), quoteLiteral(table), quoteLiteral(partition))
	raw, err := observer.client.Query(ctx, queryForEndpoint(endpoint, database, query, FormatTSVRaw))
	if err != nil {
		return 0, err
	}
	return parseUint(strings.TrimSpace(raw))
}

func replicaHealthy(replica Replica) bool {
	return !replica.IsReadonly && !replica.SessionExpired && replica.FutureParts == 0 && replica.PartsToCheck == 0 && replica.QueueSize == 0 && replica.InsertsInQueue == 0 && replica.MergesInQueue == 0 && replica.LogLag == 0 && replica.AbsoluteDelay == 0
}
