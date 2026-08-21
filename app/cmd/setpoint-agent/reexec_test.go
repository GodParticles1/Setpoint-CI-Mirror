package main

import (
	"reflect"
	"testing"
)

func TestEnvironmentWithoutEnrollmentToken(t *testing.T) {
	environment := []string{
		"PATH=/usr/bin",
		"SETPOINT_AGENT_ENROLLMENT_TOKEN=must-not-survive-reexec",
		"SETPOINT_AGENT_SERVER_URL=http://127.0.0.1:8080",
	}
	want := []string{
		"PATH=/usr/bin",
		"SETPOINT_AGENT_SERVER_URL=http://127.0.0.1:8080",
	}

	if got := environmentWithoutEnrollmentToken(environment); !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered environment=%q, want %q", got, want)
	}
}
