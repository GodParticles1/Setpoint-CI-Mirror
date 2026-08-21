package clickhouse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"setpoint/internal/operation"
)

type Endpoint struct {
	Host   string `json:"host"`
	Port   uint16 `json:"port,omitempty"`
	User   string `json:"user,omitempty"`
	Secure bool   `json:"secure,omitempty"`
}

type PairParameters struct {
	Source     Endpoint `json:"source"`
	Target     Endpoint `json:"target"`
	Database   string   `json:"database"`
	Tables     []string `json:"tables"`
	Profile    string   `json:"profile,omitempty"`
	TimeColumn string   `json:"time_column,omitempty"`
	StartTime  string   `json:"start_time,omitempty"`
	EndTime    string   `json:"end_time,omitempty"`
}

func ParsePairParameters(raw json.RawMessage) (PairParameters, error) {
	if len(raw) == 0 {
		return PairParameters{}, errors.New("ClickHouse source/target parameters are required")
	}
	var pair PairParameters
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pair); err != nil {
		return PairParameters{}, fmt.Errorf("decode ClickHouse source/target parameters: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PairParameters{}, errors.New("decode ClickHouse source/target parameters: multiple JSON values are not allowed")
	}
	return normalizePairParameters(pair)
}

type CatalogDescriptor struct{}

func NewCatalogDescriptor() CatalogDescriptor { return CatalogDescriptor{} }

func (CatalogDescriptor) Metadata() operation.Metadata { return OperationMetadata() }

func (CatalogDescriptor) NormalizeParameters(raw json.RawMessage) (json.RawMessage, error) {
	pair, err := ParsePairParameters(raw)
	if err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(pair)
	if err != nil {
		return nil, fmt.Errorf("encode normalized ClickHouse source/target parameters: %w", err)
	}
	return normalized, nil
}

func normalizePairParameters(pair PairParameters) (PairParameters, error) {
	pair.Database = strings.TrimSpace(pair.Database)
	pair.Profile = strings.TrimSpace(pair.Profile)
	pair.TimeColumn = strings.TrimSpace(pair.TimeColumn)
	pair.StartTime = strings.TrimSpace(pair.StartTime)
	pair.EndTime = strings.TrimSpace(pair.EndTime)
	pair.Source = normalizeEndpoint(pair.Source)
	pair.Target = normalizeEndpoint(pair.Target)

	probe := Parameters{Role: RoleSource, Database: pair.Database, Tables: pair.Tables, Host: pair.Source.Host, Port: pair.Source.Port, User: pair.Source.User, Secure: pair.Source.Secure, Profile: pair.Profile, TimeColumn: pair.TimeColumn, StartTime: pair.StartTime, EndTime: pair.EndTime}
	normalized, err := normalizeParameters(probe)
	if err != nil {
		return PairParameters{}, err
	}
	pair.Database, pair.Tables, pair.TimeColumn = normalized.Database, normalized.Tables, normalized.TimeColumn
	pair.Source.Host, pair.Source.Port, pair.Source.User = normalized.Host, normalized.Port, normalized.User

	if pair.Target.Host == "" {
		return PairParameters{}, errors.New("target ClickHouse host is required")
	}
	if pair.Target.Port == 0 {
		if pair.Target.Secure {
			pair.Target.Port = 9440
		} else {
			pair.Target.Port = 9000
		}
	}
	if sameEndpoint(pair.Source, pair.Target) {
		return PairParameters{}, errors.New("source and target ClickHouse endpoints must be different")
	}
	if _, err := BuildTimeRangeFilter(pair.TimeColumn, pair.StartTime, pair.EndTime); err != nil {
		return PairParameters{}, err
	}
	return pair, nil
}

func normalizeEndpoint(endpoint Endpoint) Endpoint {
	endpoint.Host = strings.TrimSpace(endpoint.Host)
	endpoint.User = strings.TrimSpace(endpoint.User)
	if endpoint.Port == 0 {
		if endpoint.Secure {
			endpoint.Port = 9440
		} else {
			endpoint.Port = 9000
		}
	}
	return endpoint
}

func sameEndpoint(left, right Endpoint) bool {
	return strings.EqualFold(strings.TrimSpace(left.Host), strings.TrimSpace(right.Host)) && left.Port == right.Port && left.Secure == right.Secure
}

func (pair PairParameters) parametersFor(role Role) Parameters {
	endpoint := pair.Source
	if role == RoleTarget {
		endpoint = pair.Target
	}
	return Parameters{Role: role, Database: pair.Database, Tables: append([]string(nil), pair.Tables...), Host: endpoint.Host, Port: endpoint.Port, User: endpoint.User, Secure: endpoint.Secure, Profile: pair.Profile, TimeColumn: pair.TimeColumn, StartTime: pair.StartTime, EndTime: pair.EndTime}
}

func (pair PairParameters) timeFilter() (*TimeRangeFilter, error) {
	return BuildTimeRangeFilter(pair.TimeColumn, pair.StartTime, pair.EndTime)
}
