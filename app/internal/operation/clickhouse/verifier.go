package clickhouse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type FingerprintVerifier interface {
	Fingerprint(context.Context, Endpoint, string, Table, *TimeRangeFilter) (DataFingerprint, error)
}

type PartitionFingerprintVerifier interface {
	FingerprintPartition(context.Context, Endpoint, string, Table, string) (DataFingerprint, error)
}

type QueryFingerprintVerifier struct {
	client QueryClient
}

func NewQueryFingerprintVerifier(client QueryClient) (*QueryFingerprintVerifier, error) {
	if client == nil {
		return nil, errors.New("ClickHouse query client is required")
	}
	return &QueryFingerprintVerifier{client: client}, nil
}

type fingerprintRow struct {
	Rows      string `json:"rows"`
	HashSum64 string `json:"hash_sum64"`
	HashXor64 string `json:"hash_xor64"`
}

func (verifier *QueryFingerprintVerifier) Fingerprint(ctx context.Context, endpoint Endpoint, database string, table Table, filter *TimeRangeFilter) (DataFingerprint, error) {
	where, err := filter.whereClause()
	if err != nil {
		return DataFingerprint{}, err
	}
	return verifier.fingerprintWhere(ctx, endpoint, database, table, where)
}

func (verifier *QueryFingerprintVerifier) FingerprintPartition(ctx context.Context, endpoint Endpoint, database string, table Table, partitionID string) (DataFingerprint, error) {
	partitionID = strings.TrimSpace(partitionID)
	if partitionID == "" || len(partitionID) > 512 || strings.IndexByte(partitionID, 0) >= 0 {
		return DataFingerprint{}, errors.New("partition fingerprint requires a valid partition ID")
	}
	return verifier.fingerprintWhere(ctx, endpoint, database, table, " WHERE _partition_id = "+quoteLiteral(partitionID))
}

func (verifier *QueryFingerprintVerifier) fingerprintWhere(ctx context.Context, endpoint Endpoint, database string, table Table, where string) (DataFingerprint, error) {
	if !validIdentifier(database) || !validIdentifier(table.Name) {
		return DataFingerprint{}, errors.New("fingerprint table identifiers are invalid")
	}
	columns := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		if !validIdentifier(column.Name) {
			return DataFingerprint{}, fmt.Errorf("invalid fingerprint column %q", column.Name)
		}
		columns = append(columns, quoteIdentifier(column.Name))
	}
	if len(columns) == 0 {
		return DataFingerprint{}, errors.New("fingerprint requires at least one column")
	}
	hashExpression := fmt.Sprintf("sipHash64(%s)", strings.Join(columns, ","))
	query := fmt.Sprintf("SELECT toString(count()) AS rows, toString(sum(%s)) AS hash_sum64, toString(groupBitXor(%s)) AS hash_xor64 FROM %s.%s%s", hashExpression, hashExpression, quoteIdentifier(database), quoteIdentifier(table.Name), where)
	raw, err := verifier.client.Query(ctx, queryForEndpoint(endpoint, database, query, FormatJSONEachRow))
	if err != nil {
		return DataFingerprint{}, fmt.Errorf("query fingerprint: %w", err)
	}
	rows, err := decodeJSONEachRow[fingerprintRow](raw)
	if err != nil {
		return DataFingerprint{}, err
	}
	if len(rows) != 1 {
		return DataFingerprint{}, fmt.Errorf("fingerprint query returned %d rows", len(rows))
	}
	count, err := parseUint(rows[0].Rows)
	if err != nil {
		return DataFingerprint{}, err
	}
	return DataFingerprint{Rows: count, HashSum64: rows[0].HashSum64, HashXor64: rows[0].HashXor64}, nil
}

type VerificationResult struct {
	Passed bool            `json:"passed"`
	Source DataFingerprint `json:"source"`
	Target DataFingerprint `json:"target"`
	Reason string          `json:"reason,omitempty"`
}

func CompareFingerprints(source, target DataFingerprint) VerificationResult {
	result := VerificationResult{Passed: source.Rows == target.Rows && source.HashSum64 == target.HashSum64 && source.HashXor64 == target.HashXor64, Source: source, Target: target}
	if !result.Passed {
		result.Reason = "row count or dual hash fingerprint mismatch"
	}
	return result
}

func encodeVerification(result VerificationResult) ([]byte, error) { return json.Marshal(result) }
