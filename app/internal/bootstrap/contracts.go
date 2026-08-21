package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	ErrorHostKeyConfirmationRequired = "bootstrap_host_key_confirmation_required"
	ErrorHostKeyChanged              = "bootstrap_host_key_changed"
	ErrorHostKeyRejected             = "bootstrap_host_key_rejected"
	ErrorGatewayConnectFailed        = "bootstrap_gateway_connect_failed"
	ErrorGatewayAuthFailed           = "bootstrap_gateway_auth_failed"
	ErrorGatewayHostKeyChanged       = "bootstrap_gateway_host_key_changed"
	ErrorGatewayTargetUnreachable    = "bootstrap_gateway_target_unreachable"
	ErrorTargetConnectFailed         = "bootstrap_target_connect_failed"
	ErrorTargetAuthFailed            = "bootstrap_target_auth_failed"
	ErrorTargetHostKeyChanged        = "bootstrap_target_host_key_changed"
	ErrorAlreadyPresent              = "bootstrap_agent_already_present"
	ErrorUnsupportedArch             = "bootstrap_unsupported_arch"
	ErrorArtifactNotFound            = "bootstrap_artifact_not_found"
	ErrorArtifactHashMismatch        = "bootstrap_artifact_hash_mismatch"
	ErrorAgentRuntimeUnreachable     = "bootstrap_agent_runtime_unreachable"
	ErrorAgentStartFailed            = "bootstrap_agent_start_failed"
	ErrorEnrollmentFailed            = "bootstrap_enrollment_failed"
	ErrorHeartbeatTimeout            = "bootstrap_heartbeat_timeout"
	ErrorAdvertiseURLUnavailable     = "bootstrap_agent_advertise_url_unavailable"
	ErrorInvalidRequest              = "bootstrap_invalid_request"
)

type Error struct {
	Code    string
	Message string
	Err     error
}

func (err *Error) Error() string {
	if err == nil {
		return ""
	}
	if err.Message != "" {
		return err.Message
	}
	if err.Err != nil {
		return err.Err.Error()
	}
	return err.Code
}

func (err *Error) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type InstallProfile struct {
	Mode                string `json:"mode"`
	Root                string `json:"root"`
	BinaryPath          string `json:"binary_path"`
	ConfigPath          string `json:"config_path"`
	IdentityPath        string `json:"identity_path"`
	CredentialPath      string `json:"credential_path"`
	TaskJournalPath     string `json:"task_journal_path"`
	EnrollmentTokenPath string `json:"enrollment_token_path"`
}

func ResolveInstallProfile(goos, home string, uid int) (InstallProfile, error) {
	if strings.TrimSpace(goos) != "linux" {
		return InstallProfile{}, fmt.Errorf("unsupported bootstrap OS %q", goos)
	}
	if uid < 0 {
		return InstallProfile{}, errors.New("uid must be non-negative")
	}
	var root string
	mode := "non-root"
	if uid == 0 {
		mode = "root"
		root = "/opt/setpoint/agent"
	} else {
		home = path.Clean(strings.TrimSpace(home))
		if home == "." || !path.IsAbs(home) || home == "/" {
			return InstallProfile{}, errors.New("non-root bootstrap requires an absolute home directory")
		}
		root = path.Join(home, ".local", "share", "setpoint", "agent")
	}
	return InstallProfile{
		Mode:                mode,
		Root:                root,
		BinaryPath:          path.Join(root, "bin", "setpoint-agent"),
		ConfigPath:          path.Join(root, "config.json"),
		IdentityPath:        path.Join(root, "state", "agent-id"),
		CredentialPath:      path.Join(root, "state", "agent-credential.json"),
		TaskJournalPath:     path.Join(root, "state", "task-journal.json"),
		EnrollmentTokenPath: path.Join(root, "bootstrap", "enrollment-token"),
	}, nil
}

func ValidateAgentAdvertiseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("Agent advertise URL must be an HTTP(S) URL without user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Agent advertise URL must not contain query or fragment")
	}
	return nil
}

func ValidateRemoteAdvertiseURL(value string) error {
	if err := ValidateAgentAdvertiseURL(value); err != nil {
		return err
	}
	parsed, _ := url.Parse(strings.TrimSpace(value))
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return &Error{Code: ErrorAdvertiseURLUnavailable, Message: "Agent advertise URL is not remotely reachable"}
	}
	if strings.EqualFold(host, "localhost") {
		return &Error{Code: ErrorAdvertiseURLUnavailable, Message: "Agent advertise URL is not remotely reachable"}
	}
	return nil
}

type GatewayInput struct {
	Address                    string
	Port                       uint16
	Username                   string
	Password                   string
	ExpectedHostKeyFingerprint string
}

type ProbeInput struct {
	Address  string
	Port     uint16
	Username string
	Password string
	Gateway  *GatewayInput
}

type HostProbe struct {
	HostKeyFingerprint        string
	GatewayHostKeyFingerprint string
	OS                        string
	OSVersion                 string
	Arch                      string
	Username                  string
	UID                       int
	Home                      string
	AgentPresent              bool
	InstallProfile            InstallProfile
}

type Artifact struct {
	OS      string
	Arch    string
	Version string
	SHA256  string
	Path    string
}

type StartSpec struct {
	AgentPath  string
	ConfigPath string
}

// Transport deliberately exposes only bootstrap semantics. Implementations may
// use fixed internal commands, but callers cannot submit arbitrary shell text.
type Transport interface {
	ProbeHost(context.Context, ProbeInput) (HostProbe, error)
	PrepareStaging(context.Context, string) error
	UploadAgent(context.Context, Artifact, string) error
	WriteBootstrapFile(context.Context, string, []byte, uint32) error
	VerifyRemoteSHA256(context.Context, string, string) error
	ProbeAgentRuntime(context.Context, string, string) error
	PromoteStaging(context.Context, string, InstallProfile) error
	StartAgent(context.Context, StartSpec) error
	ReadAgentID(context.Context, string) (string, error)
	CleanupStaging(context.Context, string) error
	CleanupEnrollmentToken(context.Context, string) error
	Close() error
}

type TransportFactory interface {
	Connect(context.Context, ProbeInput, string) (Transport, error)
}

type ArtifactProvider interface {
	Select(context.Context, string, string) (Artifact, error)
}

type EnrollmentToken struct {
	ID     string
	Secret string
}

type EnrollmentAuthority interface {
	IssueBootstrapEnrollment(context.Context) (EnrollmentToken, error)
	RevokeBootstrapEnrollment(context.Context, string) error
}

type OnlineNode struct {
	ID           string
	Hostname     string
	OS           string
	OSVersion    string
	Arch         string
	AgentVersion string
	LastSeenAt   time.Time
	Online       bool
}

type Verifier interface {
	WaitOnline(context.Context, string, time.Time) (OnlineNode, error)
	AssignSite(context.Context, string, string) error
}
