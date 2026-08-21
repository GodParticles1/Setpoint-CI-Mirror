package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"setpoint/internal/protocol"
)

const maxResponseBody = 1 << 20

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (err *APIError) Error() string {
	return fmt.Sprintf("server returned HTTP %d (%s): %s", err.Status, err.Code, err.Message)
}

type Client struct {
	baseURL    *url.URL
	http       *http.Client
	credential string
	mu         sync.RWMutex
}

func NewClient(serverURL string, client *http.Client) (*Client, error) {
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	parsed, err := url.Parse(strings.TrimRight(serverURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}
	return &Client{baseURL: parsed, http: client}, nil
}

func (client *Client) Register(ctx context.Context, request protocol.RegistrationRequest) error {
	var response protocol.RegistrationResponse
	return client.post(ctx, "/api/v1/agents/"+url.PathEscape(request.AgentID)+"/register", request, &response, client.Credential())
}

func (client *Client) Heartbeat(ctx context.Context, agentID string) error {
	var response protocol.HeartbeatResponse
	return client.post(ctx, "/api/v1/agents/"+url.PathEscape(agentID)+"/heartbeat", nil, &response, client.Credential())
}

func (client *Client) post(ctx context.Context, path string, payload, response any, bearer string) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := *client.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	httpResponse, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer httpResponse.Body.Close()
	limited := io.LimitReader(httpResponse.Body, maxResponseBody)
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(limited).Decode(&envelope); err != nil {
			return &APIError{Status: httpResponse.StatusCode, Code: "unknown", Message: http.StatusText(httpResponse.StatusCode)}
		}
		return &APIError{Status: httpResponse.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if httpResponse.StatusCode == http.StatusNoContent {
		return errNoContent
	}
	if response == nil {
		return nil
	}
	if err := json.NewDecoder(limited).Decode(response); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	return nil
}
