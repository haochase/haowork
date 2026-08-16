package trace

import (
	"errors"
	"sort"
)

// Projection is an in-memory replay view; it is rebuilt from append-only history.
type Projection struct {
	byID     map[string]Envelope
	children map[string][]Envelope
}

func Replay(records []Envelope) (Projection, error) {
	ordered := make([]Envelope, len(records))
	for index, record := range records {
		ordered[index] = cloneEnvelope(record)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Sequence < ordered[right].Sequence })
	projection := Projection{byID: make(map[string]Envelope, len(ordered)), children: make(map[string][]Envelope)}
	for _, record := range ordered {
		if record.ID == "" {
			return Projection{}, errors.New("trace id is required")
		}
		if _, exists := projection.byID[record.ID]; exists {
			return Projection{}, errors.New("trace id occurs more than once")
		}
		projection.byID[record.ID] = record
		if record.ParentTraceID != "" {
			projection.children[record.ParentTraceID] = append(projection.children[record.ParentTraceID], record)
		}
	}
	for id, record := range projection.byID {
		if record.ParentTraceID == id {
			return Projection{}, errors.New("trace cannot parent itself")
		}
		seen := map[string]struct{}{id: {}}
		parentID := record.ParentTraceID
		for parentID != "" {
			if _, exists := seen[parentID]; exists {
				return Projection{}, errors.New("trace parent chain contains a cycle")
			}
			seen[parentID] = struct{}{}
			parent, exists := projection.byID[parentID]
			if !exists {
				break
			}
			parentID = parent.ParentTraceID
		}
	}
	return projection, nil
}

// Current returns the latest descendant of traceID, or the trace itself when it has no child.
func (projection Projection) Current(traceID string) (Envelope, bool) {
	current, exists := projection.byID[traceID]
	if !exists {
		return Envelope{}, false
	}
	seen := make(map[string]struct{})
	for {
		if _, exists := seen[current.ID]; exists {
			return Envelope{}, false
		}
		seen[current.ID] = struct{}{}
		children := projection.children[current.ID]
		if len(children) == 0 {
			return cloneEnvelope(current), true
		}
		current = children[len(children)-1]
	}
}

func (projection Projection) Parent(traceID string) (Envelope, bool) {
	record, exists := projection.byID[traceID]
	if !exists || record.ParentTraceID == "" {
		return Envelope{}, false
	}
	parent, exists := projection.byID[record.ParentTraceID]
	return cloneEnvelope(parent), exists
}

func (projection Projection) Children(traceID string) []Envelope {
	children := projection.children[traceID]
	result := make([]Envelope, len(children))
	for index, record := range children {
		result[index] = cloneEnvelope(record)
	}
	return result
}

// Redact produces an export-safe view. Hashes and IDs remain for audit correlation.
func Redact(record Envelope) Envelope {
	redacted := cloneEnvelope(record)
	if redacted.SenderID != "" {
		redacted.SenderID = "redacted"
	}
	for index := range redacted.ArtifactRefs {
		redacted.ArtifactRefs[index].URI = "redacted"
	}
	for index := range redacted.Artifacts {
		redacted.Artifacts[index].URI = "redacted"
	}
	return redacted
}
