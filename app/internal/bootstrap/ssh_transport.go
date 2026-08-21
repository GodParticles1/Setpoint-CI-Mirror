package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type SSHFactory struct{ ConnectTimeout time.Duration }

type sshEndpointRole string

const (
	sshRoleGateway sshEndpointRole = "gateway"
	sshRoleTarget  sshEndpointRole = "target"
)

func NewSSHFactory(connectTimeout time.Duration) (*SSHFactory, error) {
	if connectTimeout <= 0 {
		return nil, errors.New("SSH connect timeout must be positive")
	}
	return &SSHFactory{ConnectTimeout: connectTimeout}, nil
}

func (factory *SSHFactory) Connect(ctx context.Context, input ProbeInput, expectedFingerprint string) (Transport, error) {
	if input.Gateway == nil {
		client, fingerprint, err := factory.connectEndpoint(ctx, input.Address, input.Port, input.Username, input.Password, expectedFingerprint, sshRoleTarget)
		if err != nil {
			return nil, err
		}
		return &sshTransport{client: client, fingerprint: fingerprint}, nil
	}

	gateway := *input.Gateway
	gatewayClient, gatewayFingerprint, err := factory.connectEndpoint(ctx, gateway.Address, gateway.Port, gateway.Username, gateway.Password, gateway.ExpectedHostKeyFingerprint, sshRoleGateway)
	if err != nil {
		return nil, err
	}
	targetAddress := net.JoinHostPort(input.Address, strconv.Itoa(int(input.Port)))
	targetConn, err := factory.dialThroughGateway(ctx, gatewayClient, targetAddress)
	if err != nil {
		_ = gatewayClient.Close()
		return nil, &Error{Code: ErrorGatewayTargetUnreachable, Message: "Gateway could not reach target SSH", Err: err}
	}
	targetClient, targetFingerprint, err := factory.handshakeSSH(ctx, targetConn, targetAddress, input.Username, input.Password, expectedFingerprint, sshRoleTarget)
	if err != nil {
		_ = targetConn.Close()
		_ = gatewayClient.Close()
		return nil, err
	}
	return &sshTransport{client: targetClient, gateway: gatewayClient, fingerprint: targetFingerprint, gatewayFingerprint: gatewayFingerprint}, nil
}

func (factory *SSHFactory) connectEndpoint(ctx context.Context, host string, port uint16, username, password, expectedFingerprint string, role sshEndpointRole) (*ssh.Client, string, error) {
	address := net.JoinHostPort(host, strconv.Itoa(int(port)))
	dialer := net.Dialer{Timeout: factory.ConnectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, "", &Error{Code: role.connectErrorCode(), Message: role.connectErrorMessage(), Err: err}
	}
	client, fingerprint, err := factory.handshakeSSH(ctx, conn, address, username, password, expectedFingerprint, role)
	if err != nil {
		_ = conn.Close()
		return nil, "", err
	}
	return client, fingerprint, nil
}

func (factory *SSHFactory) handshakeSSH(ctx context.Context, conn net.Conn, address, username, password, expectedFingerprint string, role sshEndpointRole) (*ssh.Client, string, error) {
	deadline := time.Now().Add(factory.ConnectTimeout)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = conn.SetDeadline(deadline)

	var fingerprint string
	var hostKeyErr error
	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			if expectedFingerprint != "" && fingerprint != expectedFingerprint {
				hostKeyErr = &Error{Code: role.hostKeyErrorCode(), Message: role.hostKeyErrorMessage()}
				return hostKeyErr
			}
			return nil
		},
		Timeout: factory.ConnectTimeout,
	}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		if hostKeyErr != nil {
			return nil, "", hostKeyErr
		}
		if fingerprint != "" {
			return nil, "", &Error{Code: role.authErrorCode(), Message: role.authErrorMessage(), Err: err}
		}
		return nil, "", &Error{Code: role.connectErrorCode(), Message: role.connectErrorMessage(), Err: err}
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(clientConn, channels, requests), fingerprint, nil
}

func (role sshEndpointRole) connectErrorCode() string {
	if role == sshRoleGateway {
		return ErrorGatewayConnectFailed
	}
	return ErrorTargetConnectFailed
}

func (role sshEndpointRole) connectErrorMessage() string {
	if role == sshRoleGateway {
		return "Gateway SSH connection failed"
	}
	return "target SSH connection failed"
}

func (role sshEndpointRole) authErrorCode() string {
	if role == sshRoleGateway {
		return ErrorGatewayAuthFailed
	}
	return ErrorTargetAuthFailed
}

func (role sshEndpointRole) authErrorMessage() string {
	if role == sshRoleGateway {
		return "Gateway SSH authentication failed"
	}
	return "target SSH authentication failed"
}

func (role sshEndpointRole) hostKeyErrorCode() string {
	if role == sshRoleGateway {
		return ErrorGatewayHostKeyChanged
	}
	return ErrorTargetHostKeyChanged
}

func (role sshEndpointRole) hostKeyErrorMessage() string {
	if role == sshRoleGateway {
		return "Gateway SSH host key changed after confirmation"
	}
	return "target SSH host key changed after confirmation"
}

func (factory *SSHFactory) dialThroughGateway(ctx context.Context, gateway *ssh.Client, targetAddress string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		conn, err := gateway.Dial("tcp", targetAddress)
		resultCh <- result{conn: conn, err: err}
	}()
	timer := time.NewTimer(factory.ConnectTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		_ = gateway.Close()
		return nil, ctx.Err()
	case <-timer.C:
		_ = gateway.Close()
		return nil, errors.New("gateway target dial timed out")
	case result := <-resultCh:
		return result.conn, result.err
	}
}

type sshTransport struct {
	client             *ssh.Client
	gateway            *ssh.Client
	fingerprint        string
	gatewayFingerprint string
}

func (transport *sshTransport) Close() error {
	var first error
	if transport.client != nil {
		first = transport.client.Close()
	}
	if transport.gateway != nil {
		if err := transport.gateway.Close(); first == nil {
			first = err
		}
	}
	return first
}

func (transport *sshTransport) ProbeHost(ctx context.Context, _ ProbeInput) (HostProbe, error) {
	kernel, err := transport.output(ctx, "uname -s")
	if err != nil {
		return HostProbe{}, fmt.Errorf("probe OS: %w", err)
	}
	archRaw, err := transport.output(ctx, "uname -m")
	if err != nil {
		return HostProbe{}, fmt.Errorf("probe architecture: %w", err)
	}
	uidRaw, err := transport.output(ctx, "id -u")
	if err != nil {
		return HostProbe{}, fmt.Errorf("probe uid: %w", err)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(uidRaw))
	if err != nil || uid < 0 {
		return HostProbe{}, fmt.Errorf("parse remote uid %q", strings.TrimSpace(uidRaw))
	}
	username, err := transport.output(ctx, "id -un")
	if err != nil {
		return HostProbe{}, fmt.Errorf("probe username: %w", err)
	}
	home, err := transport.output(ctx, `printf '%s' "$HOME"`)
	if err != nil {
		return HostProbe{}, fmt.Errorf("probe HOME: %w", err)
	}
	home = strings.Trim(strings.TrimSpace(home), `"`)
	goos := strings.ToLower(strings.TrimSpace(kernel))
	if goos != "linux" {
		return HostProbe{}, fmt.Errorf("unsupported bootstrap OS %q", goos)
	}
	profile, err := ResolveInstallProfile(goos, home, uid)
	if err != nil {
		return HostProbe{}, err
	}
	writeBase := home
	if uid == 0 {
		writeBase = "/opt"
	}
	if err := transport.run(ctx, "test -w "+shellQuote(writeBase)); err != nil {
		return HostProbe{}, fmt.Errorf("target install location is not writable: %w", err)
	}
	agentPresent := transport.run(ctx, "test -e "+shellQuote(profile.BinaryPath)) == nil
	osRelease, err := transport.output(ctx, "cat /etc/os-release")
	if err != nil {
		return HostProbe{}, fmt.Errorf("probe /etc/os-release: %w", err)
	}
	_, osVersion := parseOSRelease(osRelease)
	if osVersion == "" {
		osVersion = "unknown"
	}
	return HostProbe{
		HostKeyFingerprint:        transport.fingerprint,
		GatewayHostKeyFingerprint: transport.gatewayFingerprint,
		OS:                        goos,
		OSVersion:                 osVersion,
		Arch:                      normalizeArch(archRaw),
		Username:                  strings.TrimSpace(username),
		UID:                       uid,
		Home:                      home,
		AgentPresent:              agentPresent,
		InstallProfile:            profile,
	}, nil
}

func (transport *sshTransport) PrepareStaging(ctx context.Context, staging string) error {
	if !validStaging(staging) {
		return errors.New("invalid bootstrap staging path")
	}
	return transport.run(ctx, "umask 077; mkdir -- "+shellQuote(staging))
}

func (transport *sshTransport) UploadAgent(ctx context.Context, artifact Artifact, target string) error {
	file, err := os.Open(artifact.Path)
	if err != nil {
		return fmt.Errorf("open local Agent artifact: %w", err)
	}
	defer file.Close()
	if err := transport.runInput(ctx, "umask 077; cat > "+shellQuote(target)+" && chmod 0700 "+shellQuote(target), file); err != nil {
		return fmt.Errorf("upload Agent artifact: %w", err)
	}
	return nil
}

func (transport *sshTransport) WriteBootstrapFile(ctx context.Context, target string, contents []byte, mode uint32) error {
	if mode != 0o600 {
		return errors.New("bootstrap files must use mode 0600")
	}
	return transport.runInput(ctx, "umask 077; cat > "+shellQuote(target)+" && chmod 0600 "+shellQuote(target), bytes.NewReader(contents))
}

func (transport *sshTransport) VerifyRemoteSHA256(ctx context.Context, target, expected string) error {
	output, err := transport.output(ctx, "sha256sum -- "+shellQuote(target))
	if err != nil {
		return err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 || !strings.EqualFold(fields[0], strings.TrimSpace(expected)) {
		return errors.New("remote SHA256 mismatch")
	}
	return nil
}

func (transport *sshTransport) ProbeAgentRuntime(ctx context.Context, agentPath, serverURL string) error {
	if !validStagedAgent(agentPath) {
		return errors.New("invalid staged Agent path")
	}
	return transport.run(ctx, shellQuote(agentPath)+" runtime-probe --server-url "+shellQuote(serverURL))
}

func (transport *sshTransport) PromoteStaging(ctx context.Context, staging string, profile InstallProfile) error {
	if !validStaging(staging) {
		return errors.New("invalid bootstrap staging path")
	}
	stagedAgent, stagedConfig, stagedToken := path.Join(staging, "setpoint-agent"), path.Join(staging, "config.json"), path.Join(staging, "enrollment-token")
	command := strings.Join([]string{
		"umask 077",
		"test ! -e " + shellQuote(profile.BinaryPath),
		"install -d -m 0700 " + shellQuote(path.Dir(profile.BinaryPath)) + " " + shellQuote(path.Dir(profile.IdentityPath)) + " " + shellQuote(path.Dir(profile.EnrollmentTokenPath)),
		"mv -- " + shellQuote(stagedAgent) + " " + shellQuote(profile.BinaryPath),
		"mv -- " + shellQuote(stagedConfig) + " " + shellQuote(profile.ConfigPath),
		"mv -- " + shellQuote(stagedToken) + " " + shellQuote(profile.EnrollmentTokenPath),
		"chmod 0700 " + shellQuote(profile.BinaryPath),
		"chmod 0600 " + shellQuote(profile.ConfigPath) + " " + shellQuote(profile.EnrollmentTokenPath),
	}, " && ")
	return transport.run(ctx, command)
}

func (transport *sshTransport) StartAgent(ctx context.Context, spec StartSpec) error {
	return transport.run(ctx, "nohup "+shellQuote(spec.AgentPath)+" -config "+shellQuote(spec.ConfigPath)+" </dev/null >/dev/null 2>&1 &")
}

func (transport *sshTransport) ReadAgentID(ctx context.Context, identityPath string) (string, error) {
	return transport.output(ctx, "cat -- "+shellQuote(identityPath))
}

func (transport *sshTransport) CleanupStaging(ctx context.Context, staging string) error {
	if !validStaging(staging) {
		return errors.New("refuse cleanup outside run-owned bootstrap staging")
	}
	command := "rm -f -- " + shellQuote(path.Join(staging, "setpoint-agent")) + " " + shellQuote(path.Join(staging, "config.json")) + " " + shellQuote(path.Join(staging, "enrollment-token")) + "; rmdir -- " + shellQuote(staging) + " 2>/dev/null || true"
	return transport.run(ctx, command)
}

func (transport *sshTransport) CleanupEnrollmentToken(ctx context.Context, tokenPath string) error {
	clean := path.Clean(strings.TrimSpace(tokenPath))
	if !path.IsAbs(clean) || path.Base(clean) != "enrollment-token" || path.Base(path.Dir(clean)) != "bootstrap" {
		return errors.New("refuse cleanup outside the bootstrap enrollment token path")
	}
	return transport.run(ctx, "rm -f -- "+shellQuote(clean))
}

func (transport *sshTransport) output(ctx context.Context, command string) (string, error) {
	var output bytes.Buffer
	if err := transport.session(ctx, command, nil, &output); err != nil {
		return "", err
	}
	return strings.TrimSpace(output.String()), nil
}

func (transport *sshTransport) run(ctx context.Context, command string) error {
	return transport.session(ctx, command, nil, io.Discard)
}

func (transport *sshTransport) runInput(ctx context.Context, command string, input io.Reader) error {
	return transport.session(ctx, command, input, io.Discard)
}

func (transport *sshTransport) session(ctx context.Context, command string, input io.Reader, output io.Writer) error {
	session, err := transport.client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	if input != nil {
		session.Stdin = input
	}
	session.Stdout = output
	var stderr bytes.Buffer
	session.Stderr = &stderr
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return ctx.Err()
	case err := <-done:
		if err != nil {
			if message := strings.TrimSpace(stderr.String()); message != "" {
				return fmt.Errorf("%w: %s", err, message)
			}
			return err
		}
		return nil
	}
}

func validStaging(value string) bool {
	value = path.Clean(strings.TrimSpace(value))
	base := path.Base(value)
	return path.IsAbs(value) && strings.HasPrefix(base, ".setpoint-bootstrap-") && len(base) > len(".setpoint-bootstrap-")
}

func validStagedAgent(value string) bool {
	value = path.Clean(strings.TrimSpace(value))
	return path.Base(value) == "setpoint-agent" && validStaging(path.Dir(value))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func parseOSRelease(contents string) (string, string) {
	values := map[string]string{}
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return strings.ToLower(values["ID"]), values["VERSION_ID"]
}
