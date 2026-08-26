package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"setpoint/internal/domain"
	"setpoint/internal/operation"
	"setpoint/internal/task"
	"setpoint/internal/trustedexec"
)

const timeFormat = time.RFC3339Nano

func (store *Store) RegisterNode(ctx context.Context, registration domain.Registration) (domain.Node, error) {
	timestamp := registration.ReceivedAt.UTC().Format(timeFormat)
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Node{}, fmt.Errorf("begin node registration: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	result, err := transaction.ExecContext(ctx, `
		INSERT INTO nodes(id, hostname, os, os_version, arch, agent_version, reported_address, registered_at, last_seen_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			hostname = excluded.hostname,
			os = excluded.os,
			os_version = excluded.os_version,
			arch = excluded.arch,
			agent_version = excluded.agent_version,
			reported_address = excluded.reported_address,
			last_seen_at = excluded.last_seen_at,
			updated_at = excluded.updated_at
		WHERE nodes.retired_at IS NULL`,
		registration.AgentID, registration.Hostname, registration.OS, registration.OSVersion,
		registration.Arch, registration.AgentVersion, registration.ObservedSourceAddress, timestamp, timestamp, timestamp)
	if err != nil {
		return domain.Node{}, fmt.Errorf("upsert node: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return domain.Node{}, fmt.Errorf("read node registration count: %w", err)
	}
	if updated == 0 {
		return domain.Node{}, domain.ErrNotFound
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO agent_registrations(agent_id, registered_at) VALUES(?, ?)`,
		registration.AgentID, timestamp); err != nil {
		return domain.Node{}, fmt.Errorf("record agent registration: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return domain.Node{}, fmt.Errorf("commit node registration: %w", err)
	}
	return store.GetNode(ctx, registration.AgentID, 0)
}

func (store *Store) RecordHeartbeat(ctx context.Context, agentID string, receivedAt time.Time) error {
	timestamp := receivedAt.UTC().Format(timeFormat)
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin heartbeat: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	result, err := transaction.ExecContext(ctx,
		`UPDATE nodes SET last_seen_at = ?, updated_at = ? WHERE id = ? AND retired_at IS NULL`,
		timestamp, timestamp, agentID)
	if err != nil {
		return fmt.Errorf("update node heartbeat: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read heartbeat update count: %w", err)
	}
	if updated == 0 {
		return domain.ErrNotFound
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO agent_heartbeats(agent_id, received_at) VALUES(?, ?)`,
		agentID, timestamp); err != nil {
		return fmt.Errorf("record agent heartbeat: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit heartbeat: %w", err)
	}
	return nil
}

func (store *Store) ListNodes(ctx context.Context, onlineWithin time.Duration) ([]domain.Node, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT n.id, n.hostname, n.os, n.os_version, n.arch, n.agent_version,
			n.reported_address, COALESCE(n.site_id, ''), COALESCE(s.name, ''), n.tags_json, n.notes,
			COALESCE(s.trusted_executable_roots_json, '[]'), n.trusted_executable_roots_json,
			n.registered_at, n.last_seen_at
		FROM nodes n LEFT JOIN sites s ON s.id = n.site_id
		WHERE n.retired_at IS NULL ORDER BY n.id`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]domain.Node, 0)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		node.Status = statusAt(node.LastSeenAt, store.now(), onlineWithin)
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes: %w", err)
	}
	return nodes, nil
}

func (store *Store) GetNode(ctx context.Context, id string, onlineWithin time.Duration) (domain.Node, error) {
	row := store.db.QueryRowContext(ctx, `
		SELECT n.id, n.hostname, n.os, n.os_version, n.arch, n.agent_version,
			n.reported_address, COALESCE(n.site_id, ''), COALESCE(s.name, ''), n.tags_json, n.notes,
			COALESCE(s.trusted_executable_roots_json, '[]'), n.trusted_executable_roots_json,
			n.registered_at, n.last_seen_at
		FROM nodes n LEFT JOIN sites s ON s.id = n.site_id
		WHERE n.id = ? AND n.retired_at IS NULL`, id)
	node, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Node{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Node{}, err
	}
	node.Status = statusAt(node.LastSeenAt, store.now(), onlineWithin)
	return node, nil
}

func (store *Store) RetireNode(ctx context.Context, id string, retiredAt time.Time) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin node retirement: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	var active int
	if err := transaction.QueryRowContext(ctx,
		`SELECT 1 FROM nodes WHERE id = ? AND retired_at IS NULL`, id).Scan(&active); errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read active node before retirement: %w", err)
	}
	if err := transaction.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM tasks WHERE node_id = ? AND phase NOT IN (?, ?, ?)
	)`, id, task.PhaseCanceled, task.PhaseSucceeded, task.PhaseFailed).Scan(&active); err != nil {
		return fmt.Errorf("check node active work: %w", err)
	}
	if active != 0 {
		return domain.ErrNodeActiveWork
	}
	rows, err := transaction.QueryContext(ctx, `SELECT state FROM operation_runs WHERE node_id = ?`, id)
	if err != nil {
		return fmt.Errorf("list node operation states before retirement: %w", err)
	}
	for rows.Next() {
		var state operation.State
		if err := rows.Scan(&state); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan node operation state before retirement: %w", err)
		}
		if !operation.Terminal(state) {
			_ = rows.Close()
			return domain.ErrNodeActiveWork
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate node operation states before retirement: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close node operation states before retirement: %w", err)
	}

	timestamp := formatTime(retiredAt)
	if _, err := transaction.ExecContext(ctx, `UPDATE agent_credentials
		SET revoked_at = COALESCE(revoked_at, ?) WHERE agent_id = ?`, timestamp, id); err != nil {
		return fmt.Errorf("revoke node Agent credentials: %w", err)
	}
	result, err := transaction.ExecContext(ctx, `UPDATE nodes SET retired_at = ?, updated_at = ?
		WHERE id = ? AND retired_at IS NULL`, timestamp, timestamp, id)
	if err != nil {
		return fmt.Errorf("retire node: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read node retirement count: %w", err)
	}
	if updated == 0 {
		return domain.ErrNotFound
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit node retirement: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanNode(source scanner) (domain.Node, error) {
	var node domain.Node
	var tagsJSON, siteRootsJSON, nodeRootsJSON, registeredAt, lastSeenAt string
	if err := source.Scan(&node.ID, &node.Hostname, &node.OS, &node.OSVersion, &node.Arch,
		&node.AgentVersion, &node.ObservedSourceAddress, &node.SiteID, &node.SiteName, &tagsJSON, &node.Notes,
		&siteRootsJSON, &nodeRootsJSON,
		&registeredAt, &lastSeenAt); err != nil {
		return domain.Node{}, err
	}
	if err := json.Unmarshal([]byte(tagsJSON), &node.Tags); err != nil {
		return domain.Node{}, fmt.Errorf("decode node tags: %w", err)
	}
	if node.Tags == nil {
		node.Tags = []string{}
	}
	node.TrustedExecutableRoots = make([]trustedexec.ConfiguredRoot, 0)
	if node.SiteID != "" {
		siteRoots, err := decodeTrustedRoots(siteRootsJSON, trustedexec.ScopeSite, "site:"+node.SiteID)
		if err != nil {
			return domain.Node{}, err
		}
		node.TrustedExecutableRoots = append(node.TrustedExecutableRoots, siteRoots...)
	}
	nodeRoots, err := decodeTrustedRoots(nodeRootsJSON, trustedexec.ScopeNode, "node:"+node.ID)
	if err != nil {
		return domain.Node{}, err
	}
	node.TrustedExecutableRoots = append(node.TrustedExecutableRoots, nodeRoots...)
	node.RegisteredAt, err = time.Parse(timeFormat, registeredAt)
	if err != nil {
		return domain.Node{}, fmt.Errorf("parse node registration time: %w", err)
	}
	node.LastSeenAt, err = time.Parse(timeFormat, lastSeenAt)
	if err != nil {
		return domain.Node{}, fmt.Errorf("parse node heartbeat time: %w", err)
	}
	return node, nil
}

func (store *Store) UpdateNode(ctx context.Context, id string, update domain.NodeUpdate) (domain.Node, error) {
	current, err := store.GetNode(ctx, id, 0)
	if err != nil {
		return domain.Node{}, err
	}
	siteID, tags, notes := current.SiteID, current.Tags, current.Notes
	roots := current.TrustedExecutableRoots
	if update.SiteID != nil {
		siteID = *update.SiteID
		if siteID != "" {
			if _, err := store.GetSite(ctx, siteID); err != nil {
				return domain.Node{}, err
			}
		}
	}
	if update.Tags != nil {
		tags = append([]string(nil), (*update.Tags)...)
	}
	if update.Notes != nil {
		notes = *update.Notes
	}
	if update.TrustedExecutableRoots != nil {
		roots = *update.TrustedExecutableRoots
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return domain.Node{}, fmt.Errorf("encode node tags: %w", err)
	}
	rootsJSON, err := encodeTrustedRootPaths(roots, trustedexec.ScopeNode)
	if err != nil {
		return domain.Node{}, err
	}
	var siteValue any
	if siteID != "" {
		siteValue = siteID
	}
	result, err := store.db.ExecContext(ctx,
		`UPDATE nodes SET site_id = ?, tags_json = ?, notes = ?, trusted_executable_roots_json = ?, updated_at = ? WHERE id = ?`,
		siteValue, string(tagsJSON), notes, rootsJSON, formatTime(store.now()), id)
	if err != nil {
		return domain.Node{}, fmt.Errorf("update node metadata: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return domain.Node{}, fmt.Errorf("read node update count: %w", err)
	}
	if updated == 0 {
		return domain.Node{}, domain.ErrNotFound
	}
	return store.GetNode(ctx, id, 0)
}
func statusAt(lastSeen, now time.Time, onlineWithin time.Duration) domain.NodeStatus {
	if onlineWithin > 0 && now.Sub(lastSeen) > onlineWithin {
		return domain.NodeStatusOffline
	}
	return domain.NodeStatusOnline
}
