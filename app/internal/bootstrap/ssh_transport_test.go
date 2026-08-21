package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type sshTestServer struct {
	listener    net.Listener
	fingerprint string
	commands    map[string]sshTestReply
	seen        []string
	mu          sync.Mutex
	done        chan struct{}
}

type sshTestReply struct {
	stdout string
	status uint32
}

func newSSHTestServer(t *testing.T, password string, commands map[string]sshTestReply) *sshTestServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if string(pass) != password {
			return nil, errors.New("invalid password")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &sshTestServer{listener: listener, fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), commands: commands, done: make(chan struct{})}
	go server.serve(config)
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-server.done:
		case <-time.After(time.Second):
		}
	})
	return server
}

func (server *sshTestServer) serve(config *ssh.ServerConfig) {
	defer close(server.done)
	for {
		conn, err := server.listener.Accept()
		if err != nil {
			return
		}
		go server.serveConn(conn, config)
	}
}

func (server *sshTestServer) serveConn(raw net.Conn, config *ssh.ServerConfig) {
	connection, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		_ = raw.Close()
		return
	}
	defer connection.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go server.serveSession(channel, requests)
	}
}

func (server *sshTestServer) serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		server.mu.Lock()
		server.seen = append(server.seen, payload.Command)
		reply, ok := server.commands[payload.Command]
		if !ok && strings.Contains(payload.Command, "$HOME") {
			reply, ok = server.commands["$HOME"]
		}
		server.mu.Unlock()
		if !ok {
			reply.status = 127
		}
		_ = request.Reply(true, nil)
		if reply.stdout != "" {
			_, _ = channel.Write([]byte(reply.stdout))
		}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{reply.status}))
		return
	}
}

func (server *sshTestServer) input(password string) ProbeInput {
	host, portText, _ := net.SplitHostPort(server.listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return ProbeInput{Address: host, Port: uint16(port), Username: "bootstrap-user", Password: password}
}

func probeCommands(uid, username, home, arch string) map[string]sshTestReply {
	profile, _ := ResolveInstallProfile("linux", home, mustAtoi(uid))
	writeBase := home
	if uid == "0" {
		writeBase = "/opt"
	}
	return map[string]sshTestReply{
		"uname -s":                         {stdout: "Linux\n"},
		"uname -m":                         {stdout: arch + "\n"},
		"id -u":                            {stdout: uid + "\n"},
		"id -un":                           {stdout: username + "\n"},
		"$HOME":                            {stdout: `"` + home + `"`},
		"test -w " + shellQuote(writeBase): {},
		"test -e " + shellQuote(profile.BinaryPath): {status: 1},
		"cat /etc/os-release":                       {stdout: "ID=ubuntu\nVERSION_ID=24.04\n"},
	}
}

func mustAtoi(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func TestSSHFactoryCapturesFirstSeenHostKeyAndProbesNonRoot(t *testing.T) {
	server := newSSHTestServer(t, "correct-password", probeCommands("1039", "operator", "/home/operator", "x86_64"))
	factory, err := NewSSHFactory(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := factory.Connect(context.Background(), server.input("correct-password"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	probe, err := transport.ProbeHost(context.Background(), server.input("correct-password"))
	if err != nil {
		t.Fatal(err)
	}
	if probe.HostKeyFingerprint != server.fingerprint {
		t.Fatalf("fingerprint = %q, want %q", probe.HostKeyFingerprint, server.fingerprint)
	}
	if probe.UID != 1039 || probe.Username != "operator" || probe.Arch != "amd64" || probe.InstallProfile.Root != "/home/operator/.local/share/setpoint/agent" {
		t.Fatalf("unexpected non-root probe: %#v", probe)
	}
}

func TestSSHFactoryProbesRootFixedInstallRoot(t *testing.T) {
	server := newSSHTestServer(t, "correct-password", probeCommands("0", "root", "/root", "aarch64"))
	factory, _ := NewSSHFactory(2 * time.Second)
	transport, err := factory.Connect(context.Background(), server.input("correct-password"), server.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	probe, err := transport.ProbeHost(context.Background(), server.input("correct-password"))
	if err != nil {
		t.Fatal(err)
	}
	if probe.UID != 0 || probe.Arch != "arm64" || probe.InstallProfile.Root != "/opt/setpoint/agent" {
		t.Fatalf("unexpected root probe: %#v", probe)
	}
}

func TestSSHTransportRuntimeProbeUsesOnlyStagedAgentCommand(t *testing.T) {
	const agentPath = "/home/operator/.setpoint-bootstrap-runtime/setpoint-agent"
	const serverURL = "http://server.example.test:8081"
	expected := shellQuote(agentPath) + " runtime-probe --server-url " + shellQuote(serverURL)
	commands := probeCommands("1039", "operator", "/home/operator", "x86_64")
	commands[expected] = sshTestReply{}
	server := newSSHTestServer(t, "correct-password", commands)
	factory, _ := NewSSHFactory(time.Second)
	transport, err := factory.Connect(context.Background(), server.input("correct-password"), server.fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if err := transport.ProbeAgentRuntime(context.Background(), agentPath, serverURL); err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.seen) != 1 || server.seen[0] != expected {
		t.Fatalf("runtime probe commands=%#v, want only %q", server.seen, expected)
	}
	for _, forbidden := range []string{"curl", "wget", "nc ", "telnet", "/dev/tcp"} {
		if strings.Contains(server.seen[0], forbidden) {
			t.Fatalf("runtime probe used forbidden helper %q: %s", forbidden, server.seen[0])
		}
	}
}

func TestSSHFactoryRejectsWrongPassword(t *testing.T) {
	server := newSSHTestServer(t, "correct-password", probeCommands("1039", "operator", "/home/operator", "x86_64"))
	factory, _ := NewSSHFactory(time.Second)
	_, err := factory.Connect(context.Background(), server.input("wrong-password"), "")
	assertBootstrapErrorCode(t, err, ErrorTargetAuthFailed)
}

func TestSSHFactoryRejectsChangedHostKeyBeforeSession(t *testing.T) {
	server := newSSHTestServer(t, "correct-password", probeCommands("1039", "operator", "/home/operator", "x86_64"))
	factory, _ := NewSSHFactory(time.Second)
	_, err := factory.Connect(context.Background(), server.input("correct-password"), "SHA256:definitely-different")
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorTargetHostKeyChanged {
		t.Fatalf("host-key mismatch error = %v", err)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.seen) != 0 {
		t.Fatalf("remote session commands ran after host-key mismatch: %#v", server.seen)
	}
}

func TestSSHFactoryClassifiesDirectConnectionFailureWithoutMessageParsing(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	_ = listener.Close()
	factory, _ := NewSSHFactory(time.Second)
	_, err = factory.Connect(context.Background(), ProbeInput{Address: host, Port: uint16(port), Username: "operator", Password: "secret"}, "")
	assertBootstrapErrorCode(t, err, ErrorTargetConnectFailed)
}

func assertBootstrapErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error=%v code=%q, want %q", err, typedCode(typed), code)
	}
}

func typedCode(err *Error) string {
	if err == nil {
		return ""
	}
	return err.Code
}

func TestSSHFactoryHandshakeTimeoutIsBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	factory, _ := NewSSHFactory(50 * time.Millisecond)
	started := time.Now()
	_, err = factory.Connect(context.Background(), ProbeInput{Address: host, Port: uint16(port), Username: "operator", Password: "secret"}, "")
	if err == nil {
		t.Fatal("expected stalled SSH handshake to time out")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("SSH handshake timeout was not bounded: %s", time.Since(started))
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	default:
	}
}
