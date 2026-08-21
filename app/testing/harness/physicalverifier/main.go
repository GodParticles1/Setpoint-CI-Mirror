// Command physicalverifier validates the frozen RC1 Check contract evidence.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"setpoint/internal/checkrun"
	"setpoint/internal/plugin"
	"setpoint/internal/task"
)

type definitionsResponse struct {
	Definitions []plugin.CheckMetadata `json:"definitions"`
}

type summary struct {
	Tasks              int            `json:"tasks"`
	Results            int            `json:"results"`
	UniqueChecks       int            `json:"unique_checks"`
	Missing            int            `json:"missing"`
	Duplicate          int            `json:"duplicate"`
	Extra              int            `json:"extra"`
	Attempts           []int          `json:"attempts"`
	Statuses           map[string]int `json:"statuses"`
	ContractsValid     bool           `json:"contracts_valid"`
	SourceRefsComplete bool           `json:"source_refs_complete"`
	OwnersValid        bool           `json:"owners_valid"`
	VersionsValid      bool           `json:"versions_valid"`
	HostRoleUnknown    bool           `json:"host_role_unknown"`
	TrustedRootCount   int            `json:"trusted_root_count_per_task"`
}

func main() {
	runPath := flag.String("run", "", "final CheckRun JSON")
	definitionsPath := flag.String("definitions", "", "check definitions response JSON")
	expectedRoot := flag.String("expected-root", "", "expected configured root, empty for none")
	flag.Parse()
	if *runPath == "" || *definitionsPath == "" {
		fail(errors.New("run and definitions are required"))
	}

	var run checkrun.Resource
	readJSON(*runPath, &run)
	var response definitionsResponse
	readJSON(*definitionsPath, &response)
	if len(response.Definitions) != 71 {
		fail(fmt.Errorf("definitions=%d, want 71", len(response.Definitions)))
	}

	definitions := make(map[string]plugin.CheckMetadata, len(response.Definitions))
	for _, definition := range response.Definitions {
		if _, exists := definitions[definition.ID]; exists {
			fail(fmt.Errorf("duplicate definition %q", definition.ID))
		}
		definitions[definition.ID] = definition
	}

	report := summary{
		Tasks:              len(run.Tasks),
		Statuses:           map[string]int{},
		ContractsValid:     true,
		SourceRefsComplete: true,
		OwnersValid:        true,
		VersionsValid:      true,
		HostRoleUnknown:    false,
		TrustedRootCount:   -1,
	}
	if len(run.Tasks) != 8 || run.Status.Counts.TotalTasks != 8 {
		fail(fmt.Errorf("tasks=%d aggregate=%d, want 8", len(run.Tasks), run.Status.Counts.TotalTasks))
	}

	seen := make(map[string]int, len(definitions))
	plugins := make(map[string]struct{}, len(run.Tasks))
	for _, current := range run.Tasks {
		if current.Status.Phase != task.PhaseSucceeded || current.Status.Attempt != 1 {
			fail(fmt.Errorf("task phase=%s attempt=%d", current.Status.Phase, current.Status.Attempt))
		}
		report.Attempts = append(report.Attempts, current.Status.Attempt)
		if current.Spec.Execution == nil {
			fail(errors.New("task is missing Check execution contract"))
		}
		contract := current.Spec.Execution
		if contract.SchemaVersion != task.CheckExecutionContractVersion {
			fail(fmt.Errorf("contract schema=%d", contract.SchemaVersion))
		}
		if err := task.ValidateCheckExecutionContract(*contract, current.Spec.ContractDigest); err != nil {
			fail(fmt.Errorf("contract validation: %w", err))
		}
		if contract.PluginID != current.Spec.PluginID {
			fail(errors.New("contract owner does not match task plugin"))
		}
		plugins[contract.PluginID] = struct{}{}
		if report.TrustedRootCount == -1 {
			report.TrustedRootCount = len(contract.TrustedExecutableRoots)
		}
		if len(contract.TrustedExecutableRoots) != report.TrustedRootCount {
			fail(errors.New("trusted root count differs between tasks"))
		}
		if *expectedRoot == "" {
			if len(contract.TrustedExecutableRoots) != 0 {
				fail(errors.New("unexpected configured trusted root"))
			}
		} else {
			if len(contract.TrustedExecutableRoots) != 1 || contract.TrustedExecutableRoots[0].Path != *expectedRoot ||
				contract.TrustedExecutableRoots[0].Scope != "node" {
				fail(errors.New("configured trusted root is not the expected node root"))
			}
		}
		if contract.PluginID == "linux.network.source_route" {
			var parameters map[string]string
			if err := json.Unmarshal(contract.Parameters, &parameters); err != nil || parameters["host_role"] != "unknown" {
				fail(errors.New("source-route host_role is not frozen to unknown"))
			}
			report.HostRoleUnknown = true
		}
		if current.Result == nil {
			fail(errors.New("task is missing result"))
		}
		if err := task.ValidateCheckResult(current.Result, contract.ResultContract()); err != nil {
			fail(fmt.Errorf("result contract validation: %w", err))
		}
		report.Results += len(current.Result.Items)
		for _, snapshot := range contract.Checks {
			definition, exists := definitions[snapshot.ID]
			if !exists {
				report.Extra++
				continue
			}
			seen[snapshot.ID]++
			if len(snapshot.SourceRefs) == 0 || len(definition.SourceRefs) == 0 {
				report.SourceRefsComplete = false
			}
			if definition.PluginID != contract.PluginID {
				report.OwnersValid = false
			}
			if definition.PluginVersion != contract.PluginVersion {
				report.VersionsValid = false
			}
		}
		for _, item := range current.Result.Items {
			report.Statuses[string(item.Status)]++
		}
	}
	if len(plugins) != 8 {
		fail(fmt.Errorf("owning plugins=%d, want 8", len(plugins)))
	}
	for id := range definitions {
		switch seen[id] {
		case 0:
			report.Missing++
		case 1:
			report.UniqueChecks++
		default:
			report.Duplicate += seen[id] - 1
		}
	}
	sort.Ints(report.Attempts)
	if report.Results != 71 || report.UniqueChecks != 71 || report.Missing != 0 || report.Duplicate != 0 || report.Extra != 0 ||
		!report.ContractsValid || !report.SourceRefsComplete || !report.OwnersValid || !report.VersionsValid || !report.HostRoleUnknown ||
		report.Statuses[string(task.ItemError)] != 0 {
		fail(fmt.Errorf("RC1 contract summary failed: %+v", report))
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		fail(err)
	}
	fmt.Println(string(encoded))
}

func readJSON(path string, target any) {
	contents, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	if err := json.Unmarshal(contents, target); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
