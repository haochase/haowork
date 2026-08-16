package team

import "sort"

// TransferState is the stable, non-event view used to classify a return delta.
type TransferState struct {
	GoalVersion                           int
	LeaseID                               string
	Scope                                 []string
	GitBaseline, DesignHash, EvidenceHash string
	Terminal                              bool
}

// DetectTransferConflicts preserves the six Team conflict categories for offline returns.
func DetectTransferConflicts(current, candidate TransferState) []string {
	var conflicts []string
	if current.GoalVersion != candidate.GoalVersion {
		conflicts = append(conflicts, ConflictStaleGoal)
	}
	if current.LeaseID != candidate.LeaseID {
		conflicts = append(conflicts, ConflictLeaseReassigned)
	}
	if overlaps(current.Scope, candidate.Scope) {
		conflicts = append(conflicts, ConflictScopeOverlap)
	}
	if current.DesignHash != candidate.DesignHash {
		conflicts = append(conflicts, ConflictDesignDiverged)
	}
	if current.EvidenceHash != candidate.EvidenceHash {
		conflicts = append(conflicts, ConflictEvidenceMismatch)
	}
	if current.Terminal {
		conflicts = append(conflicts, ConflictTerminalState)
	}
	sort.Strings(conflicts)
	return conflicts
}

func overlaps(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if a == b {
				return true
			}
		}
	}
	return false
}
