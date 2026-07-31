package memory

import (
	"errors"
	"math"
	"time"
)

// Ranker turns a token overlap into one comparable number. It is a value type
// with no state beyond its half-life so a request can score thousands of items
// without allocating, and so the same inputs always produce the same score.
type Ranker struct {
	halfLife time.Duration
}

func NewRanker(halfLife time.Duration) (Ranker, error) {
	if halfLife <= 0 {
		// A zero half-life divides by zero inside the decay term and yields NaN
		// for a freshly written item. NaN then breaks the sort comparator, so
		// retrieval order would become arbitrary rather than merely wrong.
		return Ranker{}, errors.New("memory half life must be > 0")
	}
	return Ranker{halfLife: halfLife}, nil
}

// Weights are fixed rather than configurable. Retrieval quality is only
// debuggable if every install ranks the same way; an operator-tuned weight set
// turns every bug report into an unreproducible one.
const (
	weightKeyword = 0.70
	weightRecency = 0.15
	maxUseBoost   = 0.15
)

// kindBoost is a package-level lookup, not a map literal built per call: Score
// runs once per candidate per turn, and rebuilding a four-entry map there was
// pure allocation.
func kindBoost(kind Kind) float64 {
	switch kind {
	case KindConstraint:
		return 0.20
	case KindDecision:
		return 0.15
	case KindPreference:
		return 0.10
	case KindFact:
		return 0.05
	default:
		return 0
	}
}

// Score returns 0 when nothing matched, so a caller can treat "not relevant"
// and "not retrieved" as the same case. requireAll makes a query conjunctive,
// which is what a strict search asks for: every term must be present.
func (r Ranker) Score(queryTokens map[string]struct{}, item Memory, now time.Time, requireAll bool) (float64, []string) {
	if r.halfLife <= 0 || len(queryTokens) == 0 {
		return 0, nil
	}
	matched := intersect(queryTokens, item.Keywords)
	if len(matched) == 0 || (requireAll && len(matched) < len(queryTokens)) {
		return 0, nil
	}
	// Cosine-style normalization: dividing by the geometric mean of the two set
	// sizes stops a memory that lists forty keywords from outranking a precise
	// one just because it collides with more of the query.
	denominator := math.Sqrt(float64(len(queryTokens)) * float64(max(1, len(item.Keywords))))
	keyword := float64(len(matched)) / denominator
	age := now.Sub(item.UpdatedAt)
	if age < 0 {
		// A future timestamp (clock skew, restored backup) must not earn more
		// than a brand new item, or one bad file would pin itself to the top.
		age = 0
	}
	recency := math.Exp(-math.Ln2 * age.Seconds() / r.halfLife.Seconds())
	useBoost := math.Min(math.Log1p(float64(item.UseCount))/20.0, maxUseBoost)
	return keyword*weightKeyword + recency*weightRecency + kindBoost(item.Kind) + useBoost, matched
}
