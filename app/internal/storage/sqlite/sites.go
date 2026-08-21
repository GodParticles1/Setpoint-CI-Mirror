package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"setpoint/internal/domain"
	"setpoint/internal/trustedexec"
)

const siteColumns = `s.id, s.name, s.description, s.trusted_executable_roots_json, s.created_at, s.updated_at,
	(SELECT COUNT(*) FROM nodes n WHERE n.site_id = s.id)`

func (store *Store) CreateSite(ctx context.Context, site domain.Site, idempotencyKey string) (domain.Site, bool, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Site{}, false, fmt.Errorf("begin site creation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	existing, err := scanSite(transaction.QueryRowContext(ctx,
		`SELECT `+siteColumns+` FROM sites s WHERE s.idempotency_key = ?`, idempotencyKey))
	if err == nil {
		if existing.Name != site.Name || existing.Description != site.Description ||
			!sameRootPaths(existing.TrustedExecutableRoots, site.TrustedExecutableRoots, trustedexec.ScopeSite) {
			return domain.Site{}, false, domain.ErrIdempotencyConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.Site{}, false, err
	}
	rootsJSON, err := encodeTrustedRootPaths(site.TrustedExecutableRoots, trustedexec.ScopeSite)
	if err != nil {
		return domain.Site{}, false, err
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO sites(id, idempotency_key, name, description, trusted_executable_roots_json, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, site.ID, idempotencyKey, site.Name, site.Description, rootsJSON,
		formatTime(site.CreatedAt), formatTime(site.UpdatedAt))
	if isUniqueConstraint(err) {
		return domain.Site{}, false, domain.ErrSiteNameConflict
	}
	if err != nil {
		return domain.Site{}, false, fmt.Errorf("insert site: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return domain.Site{}, false, fmt.Errorf("commit site creation: %w", err)
	}
	created, err := store.GetSite(ctx, site.ID)
	return created, true, err
}

func (store *Store) ListSites(ctx context.Context) ([]domain.Site, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT `+siteColumns+` FROM sites s ORDER BY s.name, s.id`)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()
	sites := make([]domain.Site, 0)
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sites: %w", err)
	}
	return sites, nil
}

func (store *Store) GetSite(ctx context.Context, id string) (domain.Site, error) {
	site, err := scanSite(store.db.QueryRowContext(ctx,
		`SELECT `+siteColumns+` FROM sites s WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Site{}, domain.ErrNotFound
	}
	return site, err
}

func (store *Store) UpdateSite(ctx context.Context, site domain.Site) (domain.Site, error) {
	rootsJSON, err := encodeTrustedRootPaths(site.TrustedExecutableRoots, trustedexec.ScopeSite)
	if err != nil {
		return domain.Site{}, err
	}
	result, err := store.db.ExecContext(ctx,
		`UPDATE sites SET name = ?, description = ?, trusted_executable_roots_json = ?, updated_at = ? WHERE id = ?`,
		site.Name, site.Description, rootsJSON, formatTime(site.UpdatedAt), site.ID)
	if isUniqueConstraint(err) {
		return domain.Site{}, domain.ErrSiteNameConflict
	}
	if err != nil {
		return domain.Site{}, fmt.Errorf("update site: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return domain.Site{}, fmt.Errorf("read site update count: %w", err)
	}
	if updated == 0 {
		return domain.Site{}, domain.ErrNotFound
	}
	return store.GetSite(ctx, site.ID)
}

func (store *Store) DeleteSite(ctx context.Context, id string) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin site deletion: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE site_id = ?`, id).Scan(&count); err != nil {
		return fmt.Errorf("count site nodes: %w", err)
	}
	if count > 0 {
		return domain.ErrSiteNotEmpty
	}
	result, err := transaction.ExecContext(ctx, `DELETE FROM sites WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete site: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read site delete count: %w", err)
	}
	if deleted == 0 {
		return domain.ErrNotFound
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit site deletion: %w", err)
	}
	return nil
}

func scanSite(source scanner) (domain.Site, error) {
	var site domain.Site
	var rootsJSON, createdAt, updatedAt string
	if err := source.Scan(&site.ID, &site.Name, &site.Description, &rootsJSON, &createdAt, &updatedAt, &site.NodeCount); err != nil {
		return domain.Site{}, err
	}
	var err error
	site.TrustedExecutableRoots, err = decodeTrustedRoots(rootsJSON, trustedexec.ScopeSite, "site:"+site.ID)
	if err != nil {
		return domain.Site{}, err
	}
	site.CreatedAt, err = parseTime(createdAt, "site creation")
	if err != nil {
		return domain.Site{}, err
	}
	site.UpdatedAt, err = parseTime(updatedAt, "site update")
	if err != nil {
		return domain.Site{}, err
	}
	return site, nil
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}
