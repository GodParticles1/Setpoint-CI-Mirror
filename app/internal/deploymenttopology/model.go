package deploymenttopology

const SchemaVersion = "setpoint.deployment-topology.v1"

type Kind string

const (
	KindStandalone Kind = "standalone"
	KindDual       Kind = "dual"
	KindCluster    Kind = "cluster"
	KindUnknown    Kind = "unknown"
)

type Status string

const (
	StatusConfirmed   Status = "confirmed"
	StatusAmbiguous   Status = "ambiguous"
	StatusUnsupported Status = "unsupported"
)

type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

type EvidenceState string

const (
	EvidenceObserved    EvidenceState = "observed"
	EvidenceUnsupported EvidenceState = "unsupported"
)

type ParticipantTrust string

const (
	ParticipantTrustLocal     ParticipantTrust = "local"
	ParticipantTrustUntrusted ParticipantTrust = "untrusted"
)

// ParticipantFact is a source-local statement about one deployment member.
// Key is an opaque, stable identity chosen by the collector. The resolver only
// merges exact keys and never guesses identity from participant counts.
type ParticipantFact struct {
	Key       string   `json:"key"`
	Hostname  string   `json:"hostname,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	Role      string   `json:"role,omitempty"`
	Local     bool     `json:"local"`
}

// Evidence is one source-attributed topology observation. KindUnknown means
// that the source produced facts but did not prove a topology kind.
type Evidence struct {
	ID           string            `json:"id"`
	Source       string            `json:"source"`
	Value        string            `json:"value"`
	Confidence   Confidence        `json:"confidence"`
	State        EvidenceState     `json:"state"`
	TopologyKind Kind              `json:"topology_kind"`
	LocalRole    string            `json:"local_role,omitempty"`
	Participants []ParticipantFact `json:"participants,omitempty"`
}

// Participant is the deterministic merge of facts that used the same key.
// Any non-local participant is discovery-only and explicitly untrusted; it is
// not authorized for SSH, deployment, or enrollment by this contract.
type Participant struct {
	Key         string           `json:"key"`
	Hostname    string           `json:"hostname,omitempty"`
	Addresses   []string         `json:"addresses"`
	Role        string           `json:"role,omitempty"`
	Local       bool             `json:"local"`
	Trust       ParticipantTrust `json:"trust"`
	EvidenceIDs []string         `json:"evidence_ids"`
}

type Finding struct {
	Code        string   `json:"code"`
	Summary     string   `json:"summary"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
}

type Conflict struct {
	Field          string   `json:"field"`
	ParticipantKey string   `json:"participant_key,omitempty"`
	Values         []string `json:"values"`
	EvidenceIDs    []string `json:"evidence_ids"`
	Summary        string   `json:"summary"`
}

// Result is the generic deployment topology contract. Plugin-specific detail
// remains outside this type and may contribute evidence without being flattened
// into generic fields.
type Result struct {
	SchemaVersion string        `json:"schema_version"`
	TopologyKind  Kind          `json:"topology_kind"`
	LocalRole     string        `json:"local_role,omitempty"`
	Participants  []Participant `json:"participants"`
	Evidence      []Evidence    `json:"evidence"`
	Status        Status        `json:"status"`
	Summary       string        `json:"summary"`
	Findings      []Finding     `json:"findings,omitempty"`
	Conflicts     []Conflict    `json:"conflicts,omitempty"`
}
