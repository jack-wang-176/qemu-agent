package memory

import "time"

// CandidateStatus records where a proposed memory is in the review workflow.
type CandidateStatus string

const (
	CandidatePending  CandidateStatus = "pending"
	CandidateApproved CandidateStatus = "approved"
	CandidateRejected CandidateStatus = "rejected"
	CandidateExpired  CandidateStatus = "expired"
)

// Candidate is a proposal emitted by an Extractor. Persistence and review are
// added separately so extraction cannot write directly to the approved store.
type Candidate struct {
	ID        string          `json:"id"`
	Kind      Kind            `json:"kind"`
	Scope     Scope           `json:"scope"`
	Content   string          `json:"content"`
	Source    string          `json:"source,omitempty"`
	Status    CandidateStatus `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	MemoryID  string          `json:"memory_id,omitempty"`
}
