package xrocketreaddress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

const OperationID = "xrocket.site.readdress"

type parameters struct {
	MasterTargetAddress string `json:"master_target_address"`
	SlaveTargetAddress  string `json:"slave_target_address"`
	VIPTargetAddress    string `json:"vip_target_address"`
	PrefixLength        int    `json:"prefix_length"`
	GatewayAddress      string `json:"gateway_address"`
}

func (value *parameters) UnmarshalJSON(data []byte) error {
	type wire struct {
		MasterTargetAddress string          `json:"master_target_address"`
		SlaveTargetAddress  string          `json:"slave_target_address"`
		VIPTargetAddress    string          `json:"vip_target_address"`
		PrefixLength        json.RawMessage `json:"prefix_length"`
		GatewayAddress      string          `json:"gateway_address"`
	}
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	prefix, err := decodePrefixLength(decoded.PrefixLength)
	if err != nil {
		return err
	}
	*value = parameters{
		MasterTargetAddress: strings.TrimSpace(decoded.MasterTargetAddress),
		SlaveTargetAddress:  strings.TrimSpace(decoded.SlaveTargetAddress),
		VIPTargetAddress:    strings.TrimSpace(decoded.VIPTargetAddress),
		PrefixLength:        prefix,
		GatewayAddress:      strings.TrimSpace(decoded.GatewayAddress),
	}
	return nil
}

func decodePrefixLength(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, errors.New("prefix_length is required")
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, errors.New("prefix_length must be an integer or decimal string")
	}
	number, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, errors.New("prefix_length must be an integer or decimal string")
	}
	return number, nil
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
		{"gateway_address", value.GatewayAddress},
	}
	parsed := make(map[string]net.IP, len(addresses))
	for _, address := range addresses {
		if address.value == "" {
			return fmt.Errorf("%s is required", address.name)
		}
		ip := net.ParseIP(address.value)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("%s must be an IPv4 address", address.name)
		}
		parsed[address.name] = ip.To4()
	}
	if value.PrefixLength < 1 || value.PrefixLength > 32 {
		return errors.New("prefix_length must be between 1 and 32")
	}
	seen := make(map[string]string, len(addresses))
	for _, address := range addresses {
		canonical := parsed[address.name].String()
		if existing, duplicate := seen[canonical]; duplicate {
			return fmt.Errorf("%s collides with %s", address.name, existing)
		}
		seen[canonical] = address.name
	}
	mask := net.CIDRMask(value.PrefixLength, 32)
	network := parsed["master_target_address"].Mask(mask)
	for _, name := range []string{"slave_target_address", "vip_target_address", "gateway_address"} {
		if !parsed[name].Mask(mask).Equal(network) {
			return fmt.Errorf("%s is outside the master target subnet", name)
		}
	}
	for _, name := range []string{"master_target_address", "slave_target_address", "vip_target_address", "gateway_address"} {
		if unusableHostAddress(parsed[name], network, mask) {
			return fmt.Errorf("%s is a network or broadcast address", name)
		}
	}
	return nil
}

func unusableHostAddress(address, network net.IP, mask net.IPMask) bool {
	if address.Equal(network) {
		return true
	}
	broadcast := make(net.IP, len(network))
	for index := range network {
		broadcast[index] = network[index] | ^mask[index]
	}
	return address.Equal(broadcast)
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
	Unresolved             []string `json:"unresolved,omitempty"`
}

func (state discoveryState) resolved() bool { return len(state.Unresolved) == 0 }
