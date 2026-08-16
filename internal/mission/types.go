package mission

import (
	"time"

	"github.com/haochase/haowork/internal/model"
)

type SkillGrant = model.MissionSkillGrant
type Envelope model.MissionEnvelope

type BuildInput struct {
	ID                 string
	ProjectID          string
	Context            model.ContextSlice
	Lease              model.Lease
	GoalVersion        int
	TaskIDs            []string
	CompletionCriteria []string
	AllowedScopes      []string
	Skills             []SkillGrant
	Assignments        map[model.AgentFunction]string
	RiskLevel          string
	EnvironmentID      string
	PolicyVersion      string
	IssuedAt           time.Time
	Deadline           time.Time
}
