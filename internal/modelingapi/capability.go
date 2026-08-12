package modelingapi

// capability.go defines the discoverable capability contract.
//
// Design principles :
//   - The Engine supplies capabilities; A1 does not hard-code a global list.
//   - Operation names are valid, unique, sorted, and defensively copied.
//   - Capabilities never contain local paths or provider secrets.
//   - The contract is not permanently tied to the current five-stage pipeline.

import "sort"

// Capabilities describes the operations and features exposed by the current Engine.
type Capabilities struct {
	APIVersion       string                // For example, "v1".
	EngineName       string                // For example, "current-pipeline".
	EngineVersion    string                // Version reported by the Engine.
	Operations       []OperationDescriptor // Unique and sorted by name.
	ArtifactKinds    []string              // For example, plan, regir, code, diff, evidence.
	SupportsApply    bool
	SupportsEvidence bool
	SupportsCancel   bool
	SupportsProgress bool
}

// OperationDescriptor describes one discoverable operation.
type OperationDescriptor struct {
	Name            OperationName
	DisplayName     string
	Description     string
	RequiresSources bool
	Mutating        bool
	MayBlock        bool // Whether approval or another wait condition may block it.
}

// ValidateCapabilities checks capability consistency and safety constraints.
func ValidateCapabilities(c Capabilities) error {
	if c.APIVersion == "" {
		return errMissing("api_version")
	}
	if hasControlChar(c.APIVersion) || len(c.APIVersion) > MaxIDBytes {
		return errTooLong()
	}
	if c.EngineName == "" {
		return errMissing("engine_name")
	}
	if hasControlChar(c.EngineName) || len(c.EngineName) > MaxIDBytes {
		return errTooLong()
	}
	if c.EngineVersion == "" {
		return errMissing("engine_version")
	}
	if hasControlChar(c.EngineVersion) || len(c.EngineVersion) > MaxIDBytes {
		return errTooLong()
	}
	// Operations must be valid, unique, and sorted.
	seen := make(map[OperationName]struct{}, len(c.Operations))
	for index, op := range c.Operations {
		if err := ValidateOperationName(op.Name); err != nil {
			return err
		}
		if _, dup := seen[op.Name]; dup {
			return errInvalid("modelingapi: duplicate operation: " + string(op.Name))
		}
		seen[op.Name] = struct{}{}
		if index > 0 && c.Operations[index-1].Name >= op.Name {
			return errInvalid("modelingapi: operations must be sorted")
		}
		if op.DisplayName != "" && (hasControlChar(op.DisplayName) || len(op.DisplayName) > MaxSummaryBytes) {
			return errTooLong()
		}
		if op.Description != "" && (hasControlChar(op.Description) || len(op.Description) > MaxSummaryBytes) {
			return errTooLong()
		}
	}
	// Artifact kinds must be valid, unique, and sorted.
	kinds := make(map[string]struct{}, len(c.ArtifactKinds))
	for index, k := range c.ArtifactKinds {
		if k == "" {
			return errMissing("artifact_kind")
		}
		if hasControlChar(k) || len(k) > MaxIDBytes {
			return errTooLong()
		}
		if _, exists := kinds[k]; exists {
			return errInvalid("modelingapi: duplicate artifact kind")
		}
		kinds[k] = struct{}{}
		if index > 0 && c.ArtifactKinds[index-1] >= k {
			return errInvalid("modelingapi: artifact kinds must be sorted")
		}
	}
	return nil
}

// SortedOperations returns a copy sorted by Name.
func SortedOperations(ops []OperationDescriptor) []OperationDescriptor {
	out := make([]OperationDescriptor, len(ops))
	copy(out, ops)
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].Name) < string(out[j].Name)
	})
	return out
}
