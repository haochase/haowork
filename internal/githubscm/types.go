package githubscm

import "time"

type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Visibility    string `json:"visibility"`
}

type Reference struct {
	Ref    string          `json:"ref"`
	Object ReferenceObject `json:"object"`
}

type ReferenceObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type PullRequest struct {
	Number         int           `json:"number"`
	State          string        `json:"state"`
	Draft          bool          `json:"draft"`
	Title          string        `json:"title"`
	User           GitHubUser    `json:"user"`
	Base           PullReference `json:"base"`
	Head           PullReference `json:"head"`
	MergeCommitSHA string        `json:"merge_commit_sha"`
	MergedAt       *time.Time    `json:"merged_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

type GitHubUser struct {
	Login string `json:"login"`
}

type PullReference struct {
	Ref  string           `json:"ref"`
	SHA  string           `json:"sha"`
	Repo GitHubRepository `json:"repo"`
}

type GitHubRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type PullCommit struct {
	SHA string `json:"sha"`
}

type PullReview struct {
	ID          int64      `json:"id"`
	State       string     `json:"state"`
	CommitID    string     `json:"commit_id"`
	User        GitHubUser `json:"user"`
	SubmittedAt time.Time  `json:"submitted_at"`
}

type CheckRunsEnvelope struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []CheckRun `json:"check_runs"`
}

type CheckRun struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	HeadSHA     string     `json:"head_sha"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type CombinedStatus struct {
	State      string         `json:"state"`
	SHA        string         `json:"sha"`
	TotalCount int            `json:"total_count"`
	Statuses   []CommitStatus `json:"statuses"`
}

type CommitStatus struct {
	ID        int64     `json:"id"`
	State     string    `json:"state"`
	Context   string    `json:"context"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ResponseMeta struct {
	ETag               string
	NotModified        bool
	NextURL            string
	RateLimitRemaining int
	RateLimitReset     time.Time
}

type RepositoryResult struct {
	Repository Repository
	Meta       ResponseMeta
}

type ReferenceResult struct {
	Reference Reference
	Meta      ResponseMeta
}

type PullQuery struct {
	State     string
	Head      string
	Base      string
	Sort      string
	Direction string
	PerPage   int
	Page      int
}

type PullPageResult struct {
	Pulls []PullRequest
	Meta  ResponseMeta
}

type PullResult struct {
	Pull PullRequest
	Meta ResponseMeta
}

type CommitPageResult struct {
	Commits []PullCommit
	Meta    ResponseMeta
}

type ReviewPageResult struct {
	Reviews []PullReview
	Meta    ResponseMeta
}

type CheckPageResult struct {
	CheckRuns []CheckRun
	Meta      ResponseMeta
}

type StatusResult struct {
	Status CombinedStatus
	Meta   ResponseMeta
}
