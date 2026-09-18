package xrocketreaddress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"setpoint/internal/executor"
)

func TestParseGoldendbUsesOnlyAuthoritativeFieldsInTopLevelBlock(t *testing.T) {
	content := `
other:
  address: 10.10.10.10
  port: 9999
goldendb:
  address: 192.0.2.80
  port: 8888
  username: app
  password: "203.0.113.70"
  database: "198.51.100.71"
  schema: "192.0.2.72"
next:
  address: 203.0.113.9
`
	observation, err := parseGoldendb(content)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Address != "192.0.2.80" || observation.Port != 8888 {
		t.Fatalf("observation=%#v", observation)
	}
}

func TestParseGoldendbRejectsConflictingAuthoritativeEndpoints(t *testing.T) {
	content := `
goldendb:
  host: 192.0.2.80
  address: 192.0.2.81
  port: 8888
  password: 203.0.113.70
`
	if _, err := parseGoldendb(content); err == nil {
		t.Fatal("conflicting authoritative GoldendB endpoints were accepted")
	}
}

func TestParseGoldendbRejectsProtectedFieldAsOnlyEndpointEvidence(t *testing.T) {
	content := `
goldendb:
  password: 192.0.2.80
  database: 198.51.100.81
  schema: 203.0.113.82
  port: 8888
`
	if _, err := parseGoldendb(content); err == nil {
		t.Fatal("protected GoldendB fields were accepted as endpoint evidence")
	}
}

func TestParseIfcfgAcceptsOnlyExactIPADDRForms(t *testing.T) {
	for name, content := range map[string]string{
		"prefix":  "DEVICE=eth0\nIPADDR=192.0.2.10\nPREFIX=24\nGATEWAY=192.0.2.1\n",
		"netmask": "DEVICE=eth0\nIPADDR0=192.0.2.10\nNETMASK=255.255.255.0\nGATEWAY=192.0.2.1\n",
		"numeric": "DEVICE=eth0\nIPADDR12=192.0.2.10\nPREFIX=24\nGATEWAY=192.0.2.1\n",
	} {
		t.Run(name, func(t *testing.T) {
			address, prefix, gateway, err := parseIfcfg(content)
			if err != nil {
				t.Fatal(err)
			}
			if address != "192.0.2.10" || prefix != 24 || gateway != "192.0.2.1" {
				t.Fatalf("address=%s prefix=%d gateway=%s", address, prefix, gateway)
			}
		})
	}
	for name, content := range map[string]string{
		"near-spelling": "IPADDRESS=192.0.2.10\nPREFIX=24\nGATEWAY=192.0.2.1\n",
		"suffix":        "IPADDR_FOO=192.0.2.10\nPREFIX=24\nGATEWAY=192.0.2.1\n",
		"ambiguous":     "IPADDR=192.0.2.10\nIPADDR1=192.0.2.11\nPREFIX=24\nGATEWAY=192.0.2.1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := parseIfcfg(content); err == nil {
				t.Fatal("invalid persistent address key set was accepted")
			}
		})
	}
}

func TestParseEtcdConfigAndSingletonMemberUseDynamicPorts(t *testing.T) {
	config := `
ETCD_ADVERTISE_CLIENT_URLS=http://192.0.2.10:12379
ETCD_INITIAL_ADVERTISE_PEER_URLS=http://192.0.2.10:12380
`
	observation, err := parseEtcdConfig(config, "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	if observation.ClientPort != 12379 || observation.PeerPort != 12380 || observation.Scheme != "http" {
		t.Fatalf("observation=%#v", observation)
	}
	memberID, count, err := parseEtcdMemberList(`{"members":[{"ID":4660,"peerURLs":["http://192.0.2.10:12380"]}]}`, "192.0.2.10", 12380)
	if err != nil {
		t.Fatal(err)
	}
	if memberID != "1234" || count != 1 {
		t.Fatalf("member id=%s count=%d", memberID, count)
	}
	if _, _, err := parseEtcdMemberList(`{"members":[{"ID":1,"peerURLs":["http://192.0.2.10:12380"]},{"ID":2,"peerURLs":["http://192.0.2.11:12380"]}]}`, "192.0.2.10", 12380); err == nil {
		t.Fatal("multi-member etcd was accepted")
	}
}

func TestParseEtcdMonitServiceRequiresUniqueService(t *testing.T) {
	service, err := parseEtcdMonitService(`
'xetcd'                         Running
'xnginx'                        Running
`)
	if err != nil || service != "xetcd" {
		t.Fatalf("service=%q err=%v", service, err)
	}
	if _, err := parseEtcdMonitService("xetcd Running\netcd-secondary Running\n"); err == nil {
		t.Fatal("ambiguous etcd services were accepted")
	}
}

type identitySpyExecutor struct {
	commands []executor.Command
}

func (spy *identitySpyExecutor) Execute(_ context.Context, command executor.Command) (executor.Result, error) {
	spy.commands = append(spy.commands, command)
	if command.Name != "runuser" {
		return executor.Result{}, errors.New("unexpected command")
	}
	return executor.Result{Stdout: "0.15.458\n"}, nil
}

func TestProductExecuteUsesDiscoveredHomePrefixedIdentity(t *testing.T) {
	spy := &identitySpyExecutor{}
	probe := discoveryProbe{executor: spy}
	profile := runtimeProfile{
		InstallProfile: installProfileHomePrefix,
		ProductUser:    "product",
		ProductHome:    "/home/product",
		XrocketBinary:  "/home/product/opt/data/xrocket/xrocket.v2",
	}
	output, err := probe.productExecute(context.Background(), profile, []string{"--version"})
	if err != nil || output != "0.15.458\n" {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if len(spy.commands) != 1 || spy.commands[0].Name != "runuser" || !reflect.DeepEqual(spy.commands[0].Args,
		[]string{"-u", "product", "--", "env", "HOME=/home/product", "/home/product/opt/data/xrocket/xrocket.v2", "--version"}) {
		t.Fatalf("commands=%#v", spy.commands)
	}
}
