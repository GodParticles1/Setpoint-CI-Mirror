package deploymenttopology

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

var ErrInvalidEvidence = errors.New("invalid deployment topology evidence")

// Resolve merges source-attributed observations without contacting peers or
// inferring topology from participant count. Any contradictory positive claim
// remains visible and fails closed as ambiguous; confidence never silently
// overrides conflicting evidence.
func Resolve(input []Evidence) (Result, error) {
	evidence, err := normalizeEvidence(input)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		SchemaVersion: SchemaVersion,
		TopologyKind:  KindUnknown,
		Participants:  []Participant{},
		Evidence:      evidence,
	}

	observed := make([]Evidence, 0, len(evidence))
	for _, item := range evidence {
		if item.State == EvidenceObserved {
			observed = append(observed, item)
		}
	}
	if len(observed) == 0 {
		result.Status = StatusUnsupported
		result.Summary = "deployment topology discovery is unsupported by the available evidence providers"
		return result, nil
	}

	participants, participantConflicts := mergeParticipants(observed)
	result.Participants = participants
	result.Conflicts = append(result.Conflicts, participantConflicts...)

	kindClaims := make(map[Kind][]string)
	roleClaims := make(map[string][]string)
	for _, item := range observed {
		if item.TopologyKind != KindUnknown {
			kindClaims[item.TopologyKind] = append(kindClaims[item.TopologyKind], item.ID)
		}
		if item.LocalRole != "" {
			roleClaims[item.LocalRole] = append(roleClaims[item.LocalRole], item.ID)
		}
	}

	switch len(kindClaims) {
	case 0:
		result.Findings = append(result.Findings, Finding{
			Code:        "topology_kind_unresolved",
			Summary:     "available evidence did not prove standalone, dual, or cluster topology",
			EvidenceIDs: evidenceIDs(observed),
		})
	case 1:
		for kind := range kindClaims {
			result.TopologyKind = kind
		}
	default:
		values, ids := mapValuesAndEvidence(kindClaims)
		result.Conflicts = append(result.Conflicts, Conflict{
			Field:       "topology_kind",
			Values:      values,
			EvidenceIDs: ids,
			Summary:     "evidence sources reported conflicting deployment topology kinds",
		})
	}

	switch len(roleClaims) {
	case 1:
		for role := range roleClaims {
			result.LocalRole = role
		}
	case 0:
		// local_role is optional by contract.
	default:
		values, ids := mapValuesAndEvidence(roleClaims)
		result.Conflicts = append(result.Conflicts, Conflict{
			Field:       "local_role",
			Values:      values,
			EvidenceIDs: ids,
			Summary:     "evidence sources reported conflicting local deployment roles",
		})
	}

	if result.TopologyKind != KindUnknown {
		minimum := minimumParticipants(result.TopologyKind)
		if len(result.Participants) < minimum {
			result.Findings = append(result.Findings, Finding{
				Code:        "participants_incomplete",
				Summary:     fmt.Sprintf("%s topology requires at least %d proven participants; discovered %d", result.TopologyKind, minimum, len(result.Participants)),
				EvidenceIDs: evidenceIDs(observed),
			})
		}
		localParticipants := make([]Participant, 0, 1)
		for _, participant := range result.Participants {
			if participant.Local {
				localParticipants = append(localParticipants, participant)
			}
		}
		switch len(localParticipants) {
		case 0:
			result.Findings = append(result.Findings, Finding{
				Code:        "local_participant_unresolved",
				Summary:     "no discovered participant was proven to be the local Agent host",
				EvidenceIDs: evidenceIDs(observed),
			})
		case 1:
		default:
			values := make([]string, 0, len(localParticipants))
			ids := make([]string, 0)
			for _, participant := range localParticipants {
				values = append(values, participant.Key)
				ids = append(ids, participant.EvidenceIDs...)
			}
			result.Conflicts = append(result.Conflicts, Conflict{
				Field:       "participants.local",
				Values:      uniqueSorted(values),
				EvidenceIDs: uniqueSorted(ids),
				Summary:     "more than one participant was reported as the local Agent host",
			})
		}
	}

	sortFindings(result.Findings)
	sortConflicts(result.Conflicts)
	if len(result.Findings) == 0 && len(result.Conflicts) == 0 {
		result.Status = StatusConfirmed
		result.Summary = fmt.Sprintf("%s deployment topology confirmed from %d evidence records", result.TopologyKind, len(observed))
		return result, nil
	}
	result.Status = StatusAmbiguous
	if len(result.Conflicts) > 0 {
		result.Summary = "deployment topology remains ambiguous because evidence conflicts on " + result.Conflicts[0].Field
	} else {
		result.Summary = "deployment topology remains ambiguous: " + result.Findings[0].Code
	}
	return result, nil
}

func normalizeEvidence(input []Evidence) ([]Evidence, error) {
	result := make([]Evidence, 0, len(input))
	seenIDs := make(map[string]struct{}, len(input))
	for index, item := range input {
		item.ID = strings.TrimSpace(item.ID)
		item.Source = strings.TrimSpace(item.Source)
		item.Value = strings.TrimSpace(item.Value)
		item.LocalRole = strings.TrimSpace(item.LocalRole)
		if item.TopologyKind == "" {
			item.TopologyKind = KindUnknown
		}
		if item.ID == "" || item.Source == "" || item.Value == "" {
			return nil, fmt.Errorf("%w: evidence %d is missing id, source, or value", ErrInvalidEvidence, index)
		}
		if _, exists := seenIDs[item.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate evidence id %q", ErrInvalidEvidence, item.ID)
		}
		seenIDs[item.ID] = struct{}{}
		if !validKind(item.TopologyKind) {
			return nil, fmt.Errorf("%w: evidence %q has topology kind %q", ErrInvalidEvidence, item.ID, item.TopologyKind)
		}
		if !validConfidence(item.Confidence) {
			return nil, fmt.Errorf("%w: evidence %q has confidence %q", ErrInvalidEvidence, item.ID, item.Confidence)
		}
		if item.State != EvidenceObserved && item.State != EvidenceUnsupported {
			return nil, fmt.Errorf("%w: evidence %q has state %q", ErrInvalidEvidence, item.ID, item.State)
		}
		if item.State == EvidenceUnsupported && (item.TopologyKind != KindUnknown || item.LocalRole != "" || len(item.Participants) != 0) {
			return nil, fmt.Errorf("%w: unsupported evidence %q must not claim topology facts", ErrInvalidEvidence, item.ID)
		}
		participants, err := normalizeParticipantFacts(item.ID, item.Participants)
		if err != nil {
			return nil, err
		}
		item.Participants = participants
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func normalizeParticipantFacts(evidenceID string, input []ParticipantFact) ([]ParticipantFact, error) {
	result := make([]ParticipantFact, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for index, item := range input {
		item.Key = strings.TrimSpace(item.Key)
		item.Hostname = strings.TrimSpace(item.Hostname)
		item.Role = strings.TrimSpace(item.Role)
		if item.Key == "" {
			return nil, fmt.Errorf("%w: evidence %q participant %d has no key", ErrInvalidEvidence, evidenceID, index)
		}
		if _, exists := seen[item.Key]; exists {
			return nil, fmt.Errorf("%w: evidence %q repeats participant key %q", ErrInvalidEvidence, evidenceID, item.Key)
		}
		seen[item.Key] = struct{}{}
		addresses := make([]string, 0, len(item.Addresses))
		for _, address := range item.Addresses {
			address = strings.TrimSpace(address)
			if address == "" {
				return nil, fmt.Errorf("%w: evidence %q participant %q has an empty address", ErrInvalidEvidence, evidenceID, item.Key)
			}
			addresses = append(addresses, address)
		}
		item.Addresses = uniqueSorted(addresses)
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Key < result[right].Key })
	return result, nil
}

type participantAccumulator struct {
	key         string
	hostnames   map[string][]string
	roles       map[string][]string
	addresses   []string
	local       bool
	evidenceIDs []string
}

func mergeParticipants(evidence []Evidence) ([]Participant, []Conflict) {
	byKey := make(map[string]*participantAccumulator)
	for _, item := range evidence {
		for _, fact := range item.Participants {
			entry := byKey[fact.Key]
			if entry == nil {
				entry = &participantAccumulator{key: fact.Key, hostnames: make(map[string][]string), roles: make(map[string][]string)}
				byKey[fact.Key] = entry
			}
			if fact.Hostname != "" {
				entry.hostnames[fact.Hostname] = append(entry.hostnames[fact.Hostname], item.ID)
			}
			if fact.Role != "" {
				entry.roles[fact.Role] = append(entry.roles[fact.Role], item.ID)
			}
			entry.addresses = append(entry.addresses, fact.Addresses...)
			entry.local = entry.local || fact.Local
			entry.evidenceIDs = append(entry.evidenceIDs, item.ID)
		}
	}

	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	participants := make([]Participant, 0, len(keys))
	conflicts := make([]Conflict, 0)
	for _, key := range keys {
		entry := byKey[key]
		participant := Participant{
			Key:         key,
			Addresses:   uniqueSorted(entry.addresses),
			Local:       entry.local,
			EvidenceIDs: uniqueSorted(entry.evidenceIDs),
		}
		if len(entry.hostnames) == 1 {
			for value := range entry.hostnames {
				participant.Hostname = value
			}
		} else if len(entry.hostnames) > 1 {
			values, ids := mapValuesAndEvidence(entry.hostnames)
			conflicts = append(conflicts, Conflict{
				Field:          "participants.hostname",
				ParticipantKey: key,
				Values:         values,
				EvidenceIDs:    ids,
				Summary:        "the same participant key has conflicting hostnames",
			})
		}
		if len(entry.roles) == 1 {
			for value := range entry.roles {
				participant.Role = value
			}
		} else if len(entry.roles) > 1 {
			values, ids := mapValuesAndEvidence(entry.roles)
			conflicts = append(conflicts, Conflict{
				Field:          "participants.role",
				ParticipantKey: key,
				Values:         values,
				EvidenceIDs:    ids,
				Summary:        "the same participant key has conflicting roles",
			})
		}
		participants = append(participants, participant)
	}
	return participants, conflicts
}

func validKind(kind Kind) bool {
	switch kind {
	case KindStandalone, KindDual, KindCluster, KindUnknown:
		return true
	default:
		return false
	}
}

func validConfidence(confidence Confidence) bool {
	switch confidence {
	case ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
		return true
	default:
		return false
	}
}

func minimumParticipants(kind Kind) int {
	switch kind {
	case KindStandalone:
		return 1
	case KindDual, KindCluster:
		return 2
	default:
		return 0
	}
}

func evidenceIDs(evidence []Evidence) []string {
	ids := make([]string, 0, len(evidence))
	for _, item := range evidence {
		ids = append(ids, item.ID)
	}
	return uniqueSorted(ids)
}

func mapValuesAndEvidence[T ~string](claims map[T][]string) ([]string, []string) {
	values := make([]string, 0, len(claims))
	ids := make([]string, 0)
	for value, evidenceIDs := range claims {
		values = append(values, string(value))
		ids = append(ids, evidenceIDs...)
	}
	return uniqueSorted(values), uniqueSorted(ids)
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sortFindings(findings []Finding) {
	for index := range findings {
		findings[index].EvidenceIDs = uniqueSorted(findings[index].EvidenceIDs)
	}
	sort.Slice(findings, func(left, right int) bool {
		if findings[left].Code == findings[right].Code {
			return findings[left].Summary < findings[right].Summary
		}
		return findings[left].Code < findings[right].Code
	})
}

func sortConflicts(conflicts []Conflict) {
	for index := range conflicts {
		conflicts[index].Values = uniqueSorted(conflicts[index].Values)
		conflicts[index].EvidenceIDs = uniqueSorted(conflicts[index].EvidenceIDs)
	}
	sort.Slice(conflicts, func(left, right int) bool {
		if conflicts[left].Field == conflicts[right].Field {
			return conflicts[left].ParticipantKey < conflicts[right].ParticipantKey
		}
		return conflicts[left].Field < conflicts[right].Field
	})
}
