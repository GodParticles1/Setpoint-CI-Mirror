package operation

import "fmt"

type State string

const (
	StateDraft                State = "draft"
	StateDiscovering          State = "discovering"
	StatePrechecking          State = "prechecking"
	StatePlanned              State = "planned"
	StateAwaitingConfirm      State = "awaiting_confirmation"
	StateQueued               State = "queued"
	StateAcquiringLock        State = "acquiring_lock"
	StateCreatingRestorePoint State = "creating_restore_point"
	StateRunning              State = "running"
	StateVerifying            State = "verifying"
	StateSucceeded            State = "succeeded"
	StateBlocked              State = "blocked"
	StateFailed               State = "failed"
	StateRollingBack          State = "rolling_back"
	StateRolledBack           State = "rolled_back"
	StateRollbackFailed       State = "rollback_failed"
	StateInterrupted          State = "interrupted"
	StateCanceledBeforeApply  State = "canceled_before_apply"
)

var transitions = map[State]map[State]struct{}{
	StateDraft:                {StateDiscovering: {}, StateCanceledBeforeApply: {}},
	StateDiscovering:          {StatePrechecking: {}, StateBlocked: {}, StateInterrupted: {}, StateCanceledBeforeApply: {}},
	StatePrechecking:          {StatePlanned: {}, StateBlocked: {}, StateInterrupted: {}, StateCanceledBeforeApply: {}},
	StatePlanned:              {StateAwaitingConfirm: {}, StateBlocked: {}, StateCanceledBeforeApply: {}},
	StateAwaitingConfirm:      {StateQueued: {}, StateCanceledBeforeApply: {}},
	StateQueued:               {StateAcquiringLock: {}, StateCanceledBeforeApply: {}, StateInterrupted: {}, StateBlocked: {}},
	StateAcquiringLock:        {StateCreatingRestorePoint: {}, StateCanceledBeforeApply: {}, StateInterrupted: {}, StateBlocked: {}},
	StateCreatingRestorePoint: {StateRunning: {}, StateCanceledBeforeApply: {}, StateInterrupted: {}, StateBlocked: {}},
	StateRunning:              {StateVerifying: {}, StateFailed: {}, StateRollingBack: {}, StateInterrupted: {}},
	StateVerifying:            {StateSucceeded: {}, StateFailed: {}, StateRollingBack: {}, StateInterrupted: {}},
	StateFailed:               {StateRollingBack: {}},
	StateInterrupted:          {StateQueued: {}, StateRollingBack: {}, StateFailed: {}},
	StateRollingBack:          {StateRolledBack: {}, StateRollbackFailed: {}, StateInterrupted: {}},
}

func ValidState(s State) bool {
	if _, ok := transitions[s]; ok {
		return true
	}
	switch s {
	case StateSucceeded, StateBlocked, StateCanceledBeforeApply, StateRolledBack, StateRollbackFailed:
		return true
	}
	return false
}

func CanTransition(from, to State) bool {
	if !ValidState(from) || !ValidState(to) {
		return false
	}
	_, ok := transitions[from][to]
	return ok
}

func Transition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid operation transition: %s -> %s", from, to)
	}
	return nil
}

func Terminal(s State) bool {
	switch s {
	case StateSucceeded, StateBlocked, StateCanceledBeforeApply, StateRolledBack, StateRollbackFailed:
		return true
	}
	return false
}
