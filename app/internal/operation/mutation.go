package operation

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// UnmarshalJSON distinguishes an omitted optional field from an explicitly
// supplied empty/null/unknown enum on the wire. Only omission is legacy state.
func (state *MutationState) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed := MutationState(value)
	if err := ValidateMutationState(parsed, true); err != nil {
		return err
	}
	*state = parsed
	return nil
}

// ValidateMutationState does not interpret an omitted state as NOT_STARTED.
// Ordinary operations may omit it; reconnect and failed Rollback may not.
func ValidateMutationState(state MutationState, required bool) error {
	switch state {
	case MutationNotStarted, MutationChanged, MutationMayHaveChanged:
		return nil
	case "":
		if !required {
			return nil
		}
	}
	return fmt.Errorf("invalid or missing mutation state %q", state)
}

// Validate checks the accepted reboot handoff shape. BootIDAfter is absent
// until the Agent observes a new boot; its presence never changes mutation state.
func (handoff ReconnectHandoff) Validate() error {
	if handoff.Barrier != StageBarrierAgentReconnect || !handoff.Reboot || !validBootIdentity(handoff.BootIDBefore) {
		return errors.New("reconnect requires an agent_reconnect reboot handoff and boot identity")
	}
	if handoff.BootIDAfter != "" && (!validBootIdentity(handoff.BootIDAfter) || handoff.BootIDAfter == handoff.BootIDBefore) {
		return errors.New("reconnect completion requires a different boot identity")
	}
	return nil
}

func validBootIdentity(value string) bool {
	return value != "" && !strings.ContainsAny(value, " \t\r\n")
}
