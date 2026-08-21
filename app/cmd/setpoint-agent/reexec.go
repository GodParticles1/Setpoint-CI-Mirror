package main

import "strings"

const enrollmentTokenEnvironmentPrefix = "SETPOINT_AGENT_ENROLLMENT_TOKEN="

func environmentWithoutEnrollmentToken(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if strings.HasPrefix(entry, enrollmentTokenEnvironmentPrefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
