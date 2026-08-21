package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"setpoint/internal/protocol"
)

const maxRuntimeProbeResponse = 4096

func ProbeRuntime(ctx context.Context, serverURL string, client *http.Client) error {
	if client == nil {
		return errors.New("runtime probe HTTP client is required")
	}
	endpoint, err := parseServerURL(serverURL)
	if err != nil {
		return err
	}
	probeClient := *client
	probeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + protocol.AgentRuntimeReadyPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errors.New("create Agent runtime probe request")
	}
	response, err := probeClient.Do(request)
	if err != nil {
		return fmt.Errorf("send Agent runtime probe: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Agent runtime probe returned HTTP %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Agent runtime probe returned an invalid content type")
	}
	limited := io.LimitReader(response.Body, maxRuntimeProbeResponse+1)
	contents, err := io.ReadAll(limited)
	if err != nil {
		return errors.New("read Agent runtime probe response")
	}
	if len(contents) > maxRuntimeProbeResponse {
		return errors.New("Agent runtime probe response is too large")
	}
	var ready protocol.AgentRuntimeReadyResponse
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ready); err != nil {
		return errors.New("decode Agent runtime probe response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Agent runtime probe response must contain one JSON object")
	}
	if ready.Status != "ok" || ready.Service != protocol.AgentRuntimeReadyService || ready.ContractVersion != protocol.AgentRuntimeContractVersion {
		return errors.New("Agent runtime probe response does not match the required contract")
	}
	return nil
}

func parseServerURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("agent server URL must be an HTTP(S) URL without user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("agent server URL must not contain query or fragment")
	}
	return parsed, nil
}
