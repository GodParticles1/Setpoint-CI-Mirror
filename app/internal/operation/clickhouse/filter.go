package clickhouse

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type TimeRangeFilter struct {
	Column string `json:"column"`
	Start  string `json:"start"`
	End    string `json:"end"`
}

func BuildTimeRangeFilter(column, start, end string) (*TimeRangeFilter, error) {
	column = strings.TrimSpace(column)
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" && end == "" {
		return nil, nil
	}
	if !validIdentifier(column) {
		return nil, errors.New("time range requires a simple time column identifier")
	}
	if start == "" || end == "" {
		return nil, errors.New("time range requires both start and end")
	}
	startTime, err := time.Parse(time.RFC3339Nano, start)
	if err != nil { return nil, fmt.Errorf("parse start time: %w", err) }
	endTime, err := time.Parse(time.RFC3339Nano, end)
	if err != nil { return nil, fmt.Errorf("parse end time: %w", err) }
	if !startTime.Before(endTime) {
		return nil, errors.New("time range start must be before end")
	}
	return &TimeRangeFilter{Column: column, Start: startTime.UTC().Format(time.RFC3339Nano), End: endTime.UTC().Format(time.RFC3339Nano)}, nil
}

func (filter *TimeRangeFilter) whereClause() (string, error) {
	if filter == nil { return "", nil }
	normalized, err := BuildTimeRangeFilter(filter.Column, filter.Start, filter.End)
	if err != nil { return "", err }
	return fmt.Sprintf(" WHERE %s >= parseDateTime64BestEffort(%s) AND %s < parseDateTime64BestEffort(%s)", quoteIdentifier(normalized.Column), quoteLiteral(normalized.Start), quoteIdentifier(normalized.Column), quoteLiteral(normalized.End)), nil
}

func quoteIdentifier(value string) string {
	return "`" + value + "`"
}
