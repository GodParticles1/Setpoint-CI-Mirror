package bootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type sshGatewayTestServer struct {
	listener    net.Listener
	fingerprint string
	done        chan struct{}
}

type directTCPIPChannelData struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

func newSSHGatewayTestServer(t *testing.T, password string) *sshGatewayTestServer {
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
	server := &sshGatewayTestServer{listener: listener, fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), done: make(chan struct{})}
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

func (server *sshGatewayTestServer) serve(config *ssh.ServerConfig) {
	defer close(server.done)
	for {
		raw, err := server.listener.Accept()
		if err != nil {
			return
		}
		go server.serveConn(raw, config)
	}
}

func (server *sshGatewayTestServer) serveConn(raw net.Conn, config *ssh.ServerConfig) {
	connection, channels, requests, err := ssh.NewServerConn(raw, config)
	if err != nil {
		_ = raw.Close()
		return
	}
	defer connection.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "direct-tcpip" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "direct-tcpip only")
			continue
		}
		var request directTCPIPChannelData
		if err := ssh.Unmarshal(newChannel.ExtraData(), &request); err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, "invalid direct-tcpip payload")
			continue
		}
		target, err := net.DialTimeout("tcp", net.JoinHostPort(request.DestAddr, strconv.Itoa(int(request.DestPort))), time.Second)
		if err != nil {
			_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			_ = target.Close()
			continue
		}
		go ssh.DiscardRequests(channelRequests)
		go proxyGatewayChannel(channel, target)
	}
}

func proxyGatewayChannel(channel ssh.Channel, target net.Conn) {
	defer channel.Close()
	defer target.Close()
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		_, _ = io.Copy(target, channel)
		if tcp, ok := target.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	go func() {
		defer group.Done()
		_, _ = io.Copy(channel, target)
		_ = channel.CloseWrite()
	}()
	group.Wait()
}

func (server *sshGatewayTestServer) input(password string) GatewayInput {
	host, portText, _ := net.SplitHostPort(server.listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return GatewayInput{Address: host, Port: uint16(port), Username: "gateway-user", Password: password}
}

func TestSSHFactoryProbesTargetThroughGatewayAndCapturesBothHostKeys(t *testing.T) {
	target := newSSHTestServer(t, "target-password", probeCommands("0", "root", "/root", "x86_64"))
	gateway := newSSHGatewayTestServer(t, "gateway-password")
	factory, err := NewSSHFactory(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	input := target.input("target-password")
	gatewayInput := gateway.input("gateway-password")
	input.Gateway = &gatewayInput

	transport, err := factory.Connect(context.Background(), input, "")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	probe, err := transport.ProbeHost(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if probe.HostKeyFingerprint != target.fingerprint {
		t.Fatalf("target fingerprint = %q, want %q", probe.HostKeyFingerprint, target.fingerprint)
	}
	if probe.GatewayHostKeyFingerprint != gateway.fingerprint {
		t.Fatalf("gateway fingerprint = %q, want %q", probe.GatewayHostKeyFingerprint, gateway.fingerprint)
	}
	if probe.Username != "root" || probe.UID != 0 || probe.Arch != "amd64" {
		t.Fatalf("unexpected target probe through gateway: %#v", probe)
	}
}

func TestSSHFactoryRejectsChangedGatewayHostKeyBeforeTargetConnection(t *testing.T) {
	target := newSSHTestServer(t, "target-password", probeCommands("0", "root", "/root", "x86_64"))
	gateway := newSSHGatewayTestServer(t, "gateway-password")
	factory, _ := NewSSHFactory(time.Second)
	input := target.input("target-password")
	gatewayInput := gateway.input("gateway-password")
	gatewayInput.ExpectedHostKeyFingerprint = "SHA256:different-gateway"
	input.Gateway = &gatewayInput

	_, err := factory.Connect(context.Background(), input, target.fingerprint)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorGatewayHostKeyChanged {
		t.Fatalf("gateway host-key mismatch error = %v", err)
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	if len(target.seen) != 0 {
		t.Fatalf("target SSH was reached after gateway host-key mismatch: %#v", target.seen)
	}
}

func TestSSHFactoryClassifiesGatewayAuthenticationFailure(t *testing.T) {
	target := newSSHTestServer(t, "target-password", probeCommands("0", "root", "/root", "x86_64"))
	gateway := newSSHGatewayTestServer(t, "gateway-password")
	factory, _ := NewSSHFactory(time.Second)
	input := target.input("target-password")
	gatewayInput := gateway.input("wrong-gateway-password")
	input.Gateway = &gatewayInput
	_, err := factory.Connect(context.Background(), input, target.fingerprint)
	assertBootstrapErrorCode(t, err, ErrorGatewayAuthFailed)
}

func TestSSHFactoryClassifiesGatewayConnectionFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	_ = listener.Close()
	factory, _ := NewSSHFactory(time.Second)
	input := ProbeInput{Address: "127.0.0.1", Port: 22, Username: "target", Password: "target-secret"}
	input.Gateway = &GatewayInput{Address: host, Port: uint16(port), Username: "gateway", Password: "gateway-secret"}
	_, err = factory.Connect(context.Background(), input, "")
	assertBootstrapErrorCode(t, err, ErrorGatewayConnectFailed)
}

func TestSSHFactoryClassifiesGatewayTargetUnreachable(t *testing.T) {
	gateway := newSSHGatewayTestServer(t, "gateway-password")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	_ = listener.Close()
	factory, _ := NewSSHFactory(time.Second)
	input := ProbeInput{Address: host, Port: uint16(port), Username: "target", Password: "target-secret"}
	gatewayInput := gateway.input("gateway-password")
	input.Gateway = &gatewayInput
	_, err = factory.Connect(context.Background(), input, "")
	assertBootstrapErrorCode(t, err, ErrorGatewayTargetUnreachable)
}

func TestSSHFactoryClassifiesTargetAuthenticationFailureThroughGateway(t *testing.T) {
	target := newSSHTestServer(t, "target-password", probeCommands("0", "root", "/root", "x86_64"))
	gateway := newSSHGatewayTestServer(t, "gateway-password")
	factory, _ := NewSSHFactory(time.Second)
	input := target.input("wrong-target-password")
	gatewayInput := gateway.input("gateway-password")
	input.Gateway = &gatewayInput
	_, err := factory.Connect(context.Background(), input, target.fingerprint)
	assertBootstrapErrorCode(t, err, ErrorTargetAuthFailed)
}

func TestSSHFactoryClassifiesTargetHostKeyChangeThroughGateway(t *testing.T) {
	target := newSSHTestServer(t, "target-password", probeCommands("0", "root", "/root", "x86_64"))
	gateway := newSSHGatewayTestServer(t, "gateway-password")
	factory, _ := NewSSHFactory(time.Second)
	input := target.input("target-password")
	gatewayInput := gateway.input("gateway-password")
	input.Gateway = &gatewayInput
	_, err := factory.Connect(context.Background(), input, "SHA256:different-target")
	assertBootstrapErrorCode(t, err, ErrorTargetHostKeyChanged)
}
