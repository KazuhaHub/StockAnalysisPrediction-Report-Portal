package appplatform

import (
	"errors"
	"math"
)

type Action string
type Stage string
type EffectOutcome string

const (
	ActionInstall    Action = "install"
	ActionUpdate     Action = "update"
	ActionDisable    Action = "disable"
	ActionEnable     Action = "enable"
	ActionRepair     Action = "repair"
	ActionUninstall  Action = "uninstall"
	ActionDeleteData Action = "delete_data"
	ActionRestore    Action = "restore"

	StageQueued           Stage = "queued"
	StageValidate         Stage = "validate"
	StageDownload         Stage = "download"
	StageStage            Stage = "stage"
	StagePrepareData      Stage = "prepare_data"
	StageQuiesce          Stage = "quiesce"
	StageSnapshot         Stage = "snapshot"
	StageMigrate          Stage = "migrate"
	StageStart            Stage = "start"
	StageProbe            Stage = "probe"
	StageActivate         Stage = "activate"
	StageRevoke           Stage = "revoke"
	StageStop             Stage = "stop"
	StageRemoveCode       Stage = "remove_code"
	StageRemoveData       Stage = "remove_data"
	StageRestoreData      Stage = "restore_data"
	StageComplete         Stage = "complete"
	StageFailed           Stage = "failed"
	StageRecoveryRequired Stage = "recovery_required"

	EffectsNone     EffectOutcome = "none"
	EffectsReverted EffectOutcome = "reverted"
	EffectsUnknown  EffectOutcome = "unknown"
)

var (
	ErrInvalidTransition = errors.New("invalid application operation transition")
	ErrRevisionConflict  = errors.New("application operation revision conflict")
)

// Operation is a pure ordered-transition model, not an executor or durable lease.
// A future repository must atomically compare revisions and persist transitions.
// Successful validation is not evidence that an external stage actually committed.
type Operation struct {
	action     Action
	managed    bool
	generation uint64
	revision   uint64
	stage      Stage
}

type OperationSnapshot struct {
	Action     Action
	Managed    bool
	Generation uint64
	Revision   uint64
	Stage      Stage
}

func NewOperation(action Action, managed bool, generation uint64) (Operation, error) {
	if generation == 0 || len(operationStages(action, managed)) == 0 {
		return Operation{}, ErrInvalidTransition
	}
	return Operation{action: action, managed: managed, generation: generation, revision: 1, stage: StageQueued}, nil
}

// OperationFromSnapshot restores only states reachable through this model. It
// does not establish a durable lease, authority or evidence for external effects.
func OperationFromSnapshot(snapshot OperationSnapshot) (Operation, error) {
	stages := operationStages(snapshot.Action, snapshot.Managed)
	if snapshot.Generation == 0 || snapshot.Revision == 0 || len(stages) == 0 {
		return Operation{}, ErrInvalidTransition
	}
	valid := false
	if snapshot.Stage == StageFailed || snapshot.Stage == StageRecoveryRequired {
		// Failure follows one nonterminal state; it cannot precede queued or
		// follow complete. State revisions do not include lease heartbeats.
		valid = snapshot.Revision >= 2 && snapshot.Revision <= uint64(len(stages))
	} else {
		for index, stage := range stages {
			if snapshot.Stage == stage && snapshot.Revision == uint64(index+1) {
				valid = true
				break
			}
		}
	}
	if !valid {
		return Operation{}, ErrInvalidTransition
	}
	return Operation{action: snapshot.Action, managed: snapshot.Managed, generation: snapshot.Generation, revision: snapshot.Revision, stage: snapshot.Stage}, nil
}

func (o Operation) Snapshot() OperationSnapshot {
	return OperationSnapshot{Action: o.action, Managed: o.managed, Generation: o.generation, Revision: o.revision, Stage: o.stage}
}

// Advance returns a new value only for the immediate successor stage. It does not
// retry a completed effect or decide when runtime evidence is sufficient.
func (o Operation) Advance(expectedRevision uint64, next Stage) (Operation, error) {
	if err := o.checkRevision(expectedRevision); err != nil {
		return Operation{}, err
	}
	stages := operationStages(o.action, o.managed)
	for index, stage := range stages {
		if stage == o.stage && index+1 < len(stages) && stages[index+1] == next {
			o.stage, o.revision = next, o.revision+1
			return o, nil
		}
	}
	return Operation{}, ErrInvalidTransition
}

// Fail requires classified evidence from the future executor. "Reverted" must
// mean verified rollback, never a guess following an acknowledgement timeout.
func (o Operation) Fail(expectedRevision uint64, outcome EffectOutcome) (Operation, error) {
	if err := o.checkRevision(expectedRevision); err != nil {
		return Operation{}, err
	}
	if o.stage == StageComplete || o.stage == StageFailed || o.stage == StageRecoveryRequired {
		return Operation{}, ErrInvalidTransition
	}
	switch outcome {
	case EffectsNone, EffectsReverted:
		o.stage = StageFailed
	case EffectsUnknown:
		o.stage = StageRecoveryRequired
	default:
		return Operation{}, ErrInvalidTransition
	}
	o.revision++
	return o, nil
}

func (o Operation) checkRevision(expected uint64) error {
	if o.generation == 0 || o.revision == 0 || len(operationStages(o.action, o.managed)) == 0 {
		return ErrInvalidTransition
	}
	if expected != o.revision || o.revision == math.MaxUint64 {
		return ErrRevisionConflict
	}
	return nil
}

func operationStages(action Action, managed bool) []Stage {
	stages := []Stage{StageQueued, StageValidate}
	startAndProbe := func() {
		if managed {
			stages = append(stages, StageStart, StageProbe)
		}
	}
	switch action {
	case ActionInstall:
		stages = append(stages, StageDownload, StageStage, StagePrepareData)
		startAndProbe()
		stages = append(stages, StageActivate)
	case ActionUpdate:
		stages = append(stages, StageDownload, StageStage, StageQuiesce, StageSnapshot, StageMigrate)
		startAndProbe()
		stages = append(stages, StageActivate)
	case ActionDisable:
		stages = append(stages, StageRevoke)
		if managed {
			stages = append(stages, StageStop)
		}
	case ActionEnable:
		startAndProbe()
		stages = append(stages, StageActivate)
	case ActionRepair:
		stages = append(stages, StageDownload, StageStage, StageQuiesce)
		startAndProbe()
		stages = append(stages, StageActivate)
	case ActionUninstall:
		stages = append(stages, StageRevoke)
		if managed {
			stages = append(stages, StageStop)
		}
		stages = append(stages, StageRemoveCode)
	case ActionDeleteData:
		stages = append(stages, StageRevoke, StageRemoveData)
	case ActionRestore:
		stages = append(stages, StageRevoke, StageQuiesce, StageSnapshot, StageRestoreData)
		startAndProbe()
		stages = append(stages, StageActivate)
	default:
		return nil
	}
	return append(stages, StageComplete)
}
