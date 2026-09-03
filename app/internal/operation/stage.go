package operation

import (
	"errors"
	"fmt"
	"strings"
)

func ValidateExecutionStage(stage PlanStep, participants []string) error {
	stage.ID = strings.TrimSpace(stage.ID)
	stage.ExecutorNodeID = strings.TrimSpace(stage.ExecutorNodeID)
	if stage.ID == "" || stage.ExecutorNodeID == "" {
		return errors.New("physical stage requires explicit ID and executor_node_id")
	}
	participantSet := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		participantSet[participant] = struct{}{}
	}
	if _, ok := participantSet[stage.ExecutorNodeID]; !ok {
		return errors.New("physical stage executor is outside the frozen participants")
	}
	verifiedVIPOwner := false
	for _, precondition := range stage.Preconditions {
		if precondition.Kind != StagePreconditionVIPOwner {
			return fmt.Errorf("unsupported stage precondition %q", precondition.Kind)
		}
		if _, ok := participantSet[precondition.ParticipantNodeID]; !ok {
			return errors.New("stage precondition participant is outside the frozen participants")
		}
		if !precondition.Verified || len(precondition.Evidence) == 0 {
			return errors.New("stage precondition requires positive observed evidence")
		}
		if precondition.ParticipantNodeID == stage.ExecutorNodeID {
			verifiedVIPOwner = true
		}
	}
	if stage.Mutation == StageMutationVIPOwner && !verifiedVIPOwner {
		return errors.New("VIP-owner mutation requires a verified owner observation for its executor")
	}
	if stage.Barrier != "" && stage.Barrier != StageBarrierAgentReconnect {
		return fmt.Errorf("unsupported stage barrier %q", stage.Barrier)
	}
	return nil
}
