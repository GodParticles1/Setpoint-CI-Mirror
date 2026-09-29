package xrocketnodes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"setpoint/internal/deploymenttopology"
)

func TestProviderExplicitTopologyFixtures(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		local        []string
		kind         deploymenttopology.Kind
		participants int
	}{
		{
			name: "standalone ignores participant count as classifier",
			yaml: `install_mode: single
instances:
  - nodes:
      - ip: 10.0.0.1
      - ip: 10.0.0.2
`,
			local: []string{"10.0.0.1"}, kind: deploymenttopology.KindStandalone, participants: 2,
		},
		{
			name: "dual",
			yaml: `install_mode: dual
instances:
  - nodes:
      - ip: 10.0.0.1
      - ip: 10.0.0.2
`,
			local: []string{"10.0.0.2"}, kind: deploymenttopology.KindDual, participants: 2,
		},
		{
			name: "cluster",
			yaml: `install_mode: cluster
instances:
  - nodes:
      - ip: 10.0.0.1
      - ip: 10.0.0.2
      - ip: 10.0.0.3
`,
			local: []string{"10.0.0.3"}, kind: deploymenttopology.KindCluster, participants: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newForTest([]string{"/fixture/nodes.yaml"}, func(string) ([]byte, error) { return []byte(tt.yaml), nil }, func() ([]string, error) { return tt.local, nil })
			evidence, err := provider.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(evidence) != 1 {
				t.Fatalf("evidence=%d", len(evidence))
			}
			got := evidence[0]
			if got.State != deploymenttopology.EvidenceObserved || got.TopologyKind != tt.kind {
				t.Fatalf("state/kind=%s/%s", got.State, got.TopologyKind)
			}
			if len(got.Participants) != tt.participants {
				t.Fatalf("participants=%d", len(got.Participants))
			}
			locals := 0
			for _, participant := range got.Participants {
				if participant.Local {
					locals++
				}
			}
			if locals != 1 {
				t.Fatalf("locals=%d", locals)
			}
			if strings.Contains(got.Value, "password") || strings.Contains(got.Value, tt.yaml) {
				t.Fatalf("raw source leaked: %q", got.Value)
			}
		})
	}
}

func TestProviderMissingModeAndHostnameDoNotInferTopology(t *testing.T) {
	yaml := `hostname: dual-master
nodeCount: 2
instances:
  - nodes:
      - ip: 10.0.0.1
      - ip: 10.0.0.2
`
	provider := newForTest([]string{"/fixture/nodes.yaml"}, func(string) ([]byte, error) { return []byte(yaml), nil }, func() ([]string, error) { return []string{"10.0.0.1"}, nil })
	registry, err := deploymenttopology.NewProviderRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.TopologyKind != deploymenttopology.KindUnknown || result.Status != deploymenttopology.StatusUnsupported {
		t.Fatalf("unexpected inferred topology: %s/%s", result.TopologyKind, result.Status)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].State != deploymenttopology.EvidenceUnsupported || len(result.Evidence[0].Participants) != 0 {
		t.Fatalf("unsupported evidence claimed facts: %#v", result.Evidence)
	}
}

func TestProviderConflictingSourcesFailClosedThroughRegistry(t *testing.T) {
	contents := map[string]string{
		"/root/nodes.yaml": `install_mode: single
instances:
  - nodes:
      - ip: 10.0.0.1
`,
		"/app/nodes.yaml": `install_mode: dual
instances:
  - nodes:
      - ip: 10.0.0.1
      - ip: 10.0.0.2
`,
	}
	provider := newForTest([]string{"/root/nodes.yaml", "/app/nodes.yaml"}, func(path string) ([]byte, error) { return []byte(contents[path]), nil }, func() ([]string, error) { return []string{"10.0.0.1"}, nil })
	registry, err := deploymenttopology.NewProviderRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.TopologyKind != deploymenttopology.KindUnknown || result.Status != deploymenttopology.StatusAmbiguous {
		t.Fatalf("result=%s/%s", result.TopologyKind, result.Status)
	}
}

func TestProviderMissingSourceIsUnsupported(t *testing.T) {
	provider := newForTest([]string{"/missing/nodes.yaml"}, func(string) ([]byte, error) { return nil, os.ErrNotExist }, func() ([]string, error) { return []string{"10.0.0.1"}, nil })
	evidence, err := provider.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if evidence[0].State != deploymenttopology.EvidenceUnsupported || evidence[0].TopologyKind != deploymenttopology.KindUnknown {
		t.Fatalf("evidence=%#v", evidence[0])
	}
}

func TestProviderLocalAddressFailureLeavesClaimForResolverToFailClosed(t *testing.T) {
	provider := newForTest([]string{"/fixture/nodes.yaml"}, func(string) ([]byte, error) {
		return []byte("install_mode: dual\ninstances:\n  - nodes:\n      - ip: 10.0.0.1\n      - ip: 10.0.0.2\n"), nil
	}, func() ([]string, error) { return nil, errors.New("interfaces unavailable") })
	registry, err := deploymenttopology.NewProviderRegistry(provider)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.TopologyKind != deploymenttopology.KindDual || result.Status != deploymenttopology.StatusAmbiguous {
		t.Fatalf("unresolved local identity must fail closed, got %s/%s", result.TopologyKind, result.Status)
	}
	for _, evidence := range result.Evidence {
		for _, participant := range evidence.Participants {
			if participant.Local {
				t.Fatalf("local participant guessed: %#v", participant)
			}
		}
	}
}

func TestReadBoundedFileRejectsOversizedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxNodesYAMLBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedFile(path); !errors.Is(err, errFileTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestParseNodesYAMLRejectsDuplicateInstallMode(t *testing.T) {
	_, _, err := parseNodesYAML([]byte("install_mode: single\ninstall_mode: dual\n"))
	if err == nil {
		t.Fatal("expected duplicate install_mode to fail closed")
	}
}
