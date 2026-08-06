package modelingapi

// event.go defines the stable domain event contract.
//
// Design principles (v1-06, part 11):
//   - A1 defines domain events but does not implement Runstream or MCP conversion.
//   - Events are best-effort notifications; authoritative results come from Service or Show.
//   - Events never carry full diffs, Reg-IR, generated code, build logs, or artifact data.
//   - Trace, sequence, and channel fields belong to the entry transport envelope.
//   - The Pipeline publishes operation events, not request-level run_started events.

// EventKind identifies a stable event in one operation invocation.
type EventKind string

const (
	EventOperationStarted   EventKind = "operation_started"
	EventOperationProgress  EventKind = "operation_progress"
	EventOperationCompleted EventKind = "operation_completed"
)

// Progress describes bounded operation progress.
type Progress struct {
	Text    string // Bounded and free of control characters.
	Current int
	Total   int
}

// ResultSummary is the stable payload of a completed event.
type ResultSummary struct {
	Status    OperationStatus
	Summary   string // Publicly bounded and never contains artifact content.
	Artifacts []ArtifactDescriptor
	Evidence  []ArtifactDescriptor
}

// Event is one operation-level domain event.
type Event struct {
	Kind      EventKind
	ProjectID ProjectID
	Operation OperationName
	Progress  *Progress      // Progress events only.
	Result    *ResultSummary // Successful completed events only.
	Error     *PublicError   // Failed completed events only.
}

// ValidateEvent checks the payload shape for each EventKind.
//
// Rules:
//   - started carries no payload;
//   - progress carries bounded Progress and no result or error;
//   - completed carries either Result or PublicError;
//   - blocked is a completed result, not a transport failure;
//   - no field can carry artifact content.
func ValidateEvent(e Event) error {
	if _, err := ParseProjectID(string(e.ProjectID)); err != nil {
		return err
	}
	if err := ValidateOperationName(e.Operation); err != nil {
		return err
	}
	switch e.Kind {
	case EventOperationStarted:
		if e.Progress != nil || e.Result != nil || e.Error != nil {
			return errInvalid("modelingapi: started event must not carry payload")
		}
	case EventOperationProgress:
		if e.Progress == nil {
			return errMissing("progress")
		}
		if err := ValidateEventText(e.Progress.Text); err != nil {
			return err
		}
		if e.Progress.Current < 0 || e.Progress.Total < 0 {
			return errInvalid("modelingapi: negative progress")
		}
		if e.Result != nil || e.Error != nil {
			return errInvalid("modelingapi: progress event must not carry result/error")
		}
	case EventOperationCompleted:
		if e.Progress != nil {
			return errInvalid("modelingapi: completed event must not carry progress")
		}
		if e.Result != nil && e.Error != nil {
			return errInvalid("modelingapi: completed must carry either result or error, not both")
		}
		if e.Result == nil && e.Error == nil {
			return errMissing("result_or_error")
		}
		if e.Result != nil {
			switch e.Result.Status {
			case OperationSucceeded, OperationBlocked, OperationFailed:
			default:
				return errInvalid("modelingapi: unknown operation status")
			}
			if e.Result.Summary != "" {
				if err := ValidateEventText(e.Result.Summary); err != nil {
					return err
				}
			}
			for _, a := range e.Result.Artifacts {
				if err := validateArtifactDescriptor(a); err != nil {
					return err
				}
			}
			for _, a := range e.Result.Evidence {
				if err := validateArtifactDescriptor(a); err != nil {
					return err
				}
			}
		}
		if e.Error != nil {
			if err := ValidatePublicError(*e.Error); err != nil {
				return err
			}
		}
	default:
		return errInvalid("modelingapi: unknown event kind: " + string(e.Kind))
	}
	return nil
}
