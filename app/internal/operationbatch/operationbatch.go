package operationbatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	APIVersion = "setpoint.io/v1"
	Kind       = "OperationBatchConfirmation"
)

var ErrFingerprintConflict = errors.New("batch confirmation idempotency key or batch ID conflicts with a different immutable fingerprint")

type MemberState string

const (
	MemberPending            MemberState = "pending"
	MemberConfirmed          MemberState = "confirmed"
	MemberSuppressedCanceled MemberState = "suppressed_canceled"
)

type MemberIdentity struct {
	TaskID  string `json:"task_id"`
	CheckID string `json:"check_id"`
	NodeID  string `json:"node_id"`
}

type FrozenMember struct {
	Identity   MemberIdentity `json:"identity"`
	RunID      string         `json:"run_id"`
	PlanDigest string         `json:"plan_digest"`
}

type Member struct {
	Ordinal    int            `json:"ordinal"`
	Identity   MemberIdentity `json:"identity"`
	RunID      string         `json:"run_id"`
	PlanDigest string         `json:"plan_digest"`
	State      MemberState    `json:"state"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type Receipt struct {
	APIVersion                 string    `json:"api_version"`
	Kind                       string    `json:"kind"`
	BatchID                    string    `json:"batch_id"`
	SourceCheckRunID           string    `json:"source_check_run_id"`
	ConfirmationFingerprint    string    `json:"confirmation_fingerprint"`
	ConfirmationIdempotencyKey string    `json:"confirmation_idempotency_key"`
	AcceptedAt                 time.Time `json:"accepted_at"`
	Members                    []Member  `json:"members"`
}

func NewReceipt(batchID, sourceCheckRunID, confirmationKey string, members []FrozenMember, acceptedAt time.Time) (Receipt, error) {
	batchID = strings.TrimSpace(batchID)
	sourceCheckRunID = strings.TrimSpace(sourceCheckRunID)
	confirmationKey = strings.TrimSpace(confirmationKey)
	if batchID == "" || sourceCheckRunID == "" || confirmationKey == "" || len(members) == 0 || acceptedAt.IsZero() {
		return Receipt{}, errors.New("batch confirmation receipt identity, members and accepted_at are required")
	}
	fingerprint, err := Fingerprint(batchID, sourceCheckRunID, members)
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{
		APIVersion: APIVersion, Kind: Kind, BatchID: batchID, SourceCheckRunID: sourceCheckRunID,
		ConfirmationFingerprint: fingerprint, ConfirmationIdempotencyKey: confirmationKey, AcceptedAt: acceptedAt.UTC(),
		Members: make([]Member, len(members)),
	}
	for index, member := range members {
		receipt.Members[index] = Member{
			Ordinal: index, Identity: member.Identity, RunID: member.RunID, PlanDigest: member.PlanDigest,
			State: MemberPending, UpdatedAt: receipt.AcceptedAt,
		}
	}
	return receipt, nil
}

func Fingerprint(batchID, sourceCheckRunID string, members []FrozenMember) (string, error) {
	payload := struct {
		BatchID          string         `json:"batch_id"`
		SourceCheckRunID string         `json:"source_check_run_id"`
		Members          []FrozenMember `json:"members"`
	}{BatchID: strings.TrimSpace(batchID), SourceCheckRunID: strings.TrimSpace(sourceCheckRunID), Members: members}
	if payload.BatchID == "" || payload.SourceCheckRunID == "" || len(payload.Members) == 0 {
		return "", errors.New("batch confirmation fingerprint requires batch, source check run and members")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
