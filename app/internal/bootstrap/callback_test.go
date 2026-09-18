package bootstrap

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestStaticCallbackResolverKeepsLoopbackFailClosed(t *testing.T) {
	resolver, err := newStaticCallbackResolver("127.0.0.1:8081", "http://127.0.0.1:8081")
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), ProbeInput{Address: "192.0.2.20", Port: 22})
	assertCallbackErrorCode(t, err, ErrorAdvertiseURLUnavailable)
	if status := resolver.Status(); status.State != CallbackStateUnavailable || status.Mode != CallbackModeExplicit {
		t.Fatalf("unexpected explicit callback status: %#v", status)
	}
}

func TestAutomaticCallbackResolverUsesDeterministicTargetRouteSource(t *testing.T) {
	resolver, err := newAutomaticCallbackResolver("0.0.0.0:8081", func(_ context.Context, address string, port uint16) ([]net.IP, error) {
		if address != "10.20.30.40" || port != 22 {
			t.Fatalf("route lookup=%s:%d", address, port)
		}
		return []net.IP{net.ParseIP("192.168.50.10")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolver.Resolve(context.Background(), ProbeInput{Address: "10.20.30.40", Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://192.168.50.10:8081" {
		t.Fatalf("callback=%q", got)
	}
	status := resolver.Status()
	if status.Mode != CallbackModeAutomaticRoute || status.State != CallbackStateReady || !status.RuntimeProbeRequired {
		t.Fatalf("unexpected automatic callback status: %#v", status)
	}
}

func TestAutomaticCallbackResolverUsesGatewayRouteSource(t *testing.T) {
	resolver, err := newAutomaticCallbackResolver("0.0.0.0:8081", func(_ context.Context, address string, port uint16) ([]net.IP, error) {
		if address != "198.51.100.10" || port != 2222 {
			t.Fatalf("route lookup=%s:%d want gateway", address, port)
		}
		return []net.IP{net.ParseIP("172.16.20.15")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolver.Resolve(context.Background(), ProbeInput{
		Address: "10.0.0.20",
		Port:    22,
		Gateway: &GatewayInput{Address: "198.51.100.10", Port: 2222},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://172.16.20.15:8081" {
		t.Fatalf("callback=%q", got)
	}
}

func TestAutomaticCallbackResolverFailsClosedOnAmbiguousSources(t *testing.T) {
	resolver, err := newAutomaticCallbackResolver("0.0.0.0:8081", func(context.Context, string, uint16) ([]net.IP, error) {
		return []net.IP{net.ParseIP("192.168.10.5"), net.ParseIP("10.8.0.5")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), ProbeInput{Address: "node.example", Port: 22})
	assertCallbackErrorCode(t, err, ErrorAdvertiseURLUnavailable)
	if err == nil || err.Error() != "Agent callback route is ambiguous for this bootstrap target" {
		t.Fatalf("unexpected ambiguity error: %v", err)
	}
}

func TestAutomaticCallbackResolverFailsClosedWhenListenerCannotServeRouteSource(t *testing.T) {
	resolver, err := newAutomaticCallbackResolver("127.0.0.1:8081", func(context.Context, string, uint16) ([]net.IP, error) {
		return []net.IP{net.ParseIP("192.168.10.5")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), ProbeInput{Address: "192.168.10.20", Port: 22})
	assertCallbackErrorCode(t, err, ErrorAdvertiseURLUnavailable)
	if status := resolver.Status(); status.State != CallbackStateUnavailable {
		t.Fatalf("loopback listener unexpectedly ready: %#v", status)
	}
}

func TestAutomaticCallbackResolverFailsClosedWhenRouteLookupFails(t *testing.T) {
	resolver, err := newAutomaticCallbackResolver("0.0.0.0:8081", func(context.Context, string, uint16) ([]net.IP, error) {
		return nil, errors.New("no route")
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), ProbeInput{Address: "203.0.113.99", Port: 22})
	assertCallbackErrorCode(t, err, ErrorAdvertiseURLUnavailable)
}

func assertCallbackErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}
