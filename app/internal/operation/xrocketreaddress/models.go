package xrocketreaddress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

const OperationID = "xrocket.site.readdress"

type parameters struct {
	MasterTargetAddress     string `json:"master_target_address"`
	SlaveTargetAddress      string `json:"slave_target_address"`
	VIPTargetAddress        string `json:"vip_target_address"`
	ExternalDBTargetAddress string `json:"external_db_target_address"`
}

func (value *parameters) UnmarshalJSON(data []byte) error {
	type wire parameters
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	*value = parameters{
		MasterTargetAddress:     strings.TrimSpace(decoded.MasterTargetAddress),
		SlaveTargetAddress:      strings.TrimSpace(decoded.SlaveTargetAddress),
		VIPTargetAddress:        strings.TrimSpace(decoded.VIPTargetAddress),
		ExternalDBTargetAddress: strings.TrimSpace(decoded.ExternalDBTargetAddress),
	}
	return nil
}

func decodeParameters(raw json.RawMessage) (parameters, error) {
	var value parameters
	if err := json.Unmarshal(raw, &value); err != nil {
		return parameters{}, fmt.Errorf("decode xRocket readdress parameters: %w", err)
	}
	if err := validateParameters(value); err != nil {
		return parameters{}, err
	}
	return value, nil
}

func validateParameters(value parameters) error {
	addresses := []struct {
		name  string
		value string
	}{
		{"master_target_address", value.MasterTargetAddress},
		{"slave_target_address", value.SlaveTargetAddress},
		{"vip_target_address", value.VIPTargetAddress},
		{"external_db_target_address", value.ExternalDBTargetAddress},
	}
	parsed := make(map[string]string, len(addresses))
	for _, address := range addresses {
		if address.value == "" {
			return fmt.Errorf("%s is required", address.name)
		}
		ip := net.ParseIP(address.value)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("%s must be an IPv4 address", address.name)
		}
		parsed[address.name] = ip.To4().String()
	}
	seenSite := make(map[string]string, 3)
	for _, name := range []string{"master_target_address", "slave_target_address", "vip_target_address"} {
		canonical := parsed[name]
		if existing, duplicate := seenSite[canonical]; duplicate {
			return fmt.Errorf("%s collides with %s", name, existing)
		}
		seenSite[canonical] = name
	}
	return nil
}

type discoveryState struct {
	SchemaVersion          string   `json:"schema_version"`
	NodeID                 string   `json:"node_id"`
	ProductGeneration      string   `json:"product_generation,omitempty"`
	ProductVersionEvidence string   `json:"product_version_evidence,omitempty"`
	ConfiguredRole         string   `json:"configured_role,omitempty"`
	RuntimeRole            string   `json:"runtime_role,omitempty"`
	MasterAddress          string   `json:"master_address,omitempty"`
	SlaveAddress           string   `json:"slave_address,omitempty"`
	VIPAddress             string   `json:"vip_address,omitempty"`
	PrefixLength           int      `json:"prefix_length,omitempty"`
	GatewayAddress         string   `json:"gateway_address,omitempty"`
	Interface              string   `json:"interface,omitempty"`
	KeepalivedConfigPath   string   `json:"keepalived_config_path,omitempty"`
	KeepalivedPriority     int      `json:"keepalived_priority,omitempty"`
	KeepalivedNopreempt    bool     `json:"keepalived_nopreempt"`
	BusinessPorts          []int    `json:"business_ports,omitempty"`
	Unresolved             []string `json:"unresolved,omitempty"`
}

func (state discoveryState) resolved() bool { return len(state.Unresolved) == 0 }
