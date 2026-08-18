package current

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jack-wang-176/qemu-agent/internal/modeling"
	"github.com/jack-wang-176/qemu-agent/internal/pipelineapi"
)

const (
	defaultQueryLimit = 100
	maxQueryLimit     = 1000
)

// QueryAdapter exposes the read-only current modeling surface through QueryPort.
// It never writes project or artifact state.
type QueryAdapter struct {
	runner queryRunner
}

// NewQueryAdapter constructs a read-only query adapter. Query is optional when
// only Engine operations are installed, but required for every query call.
func NewQueryAdapter(runner queryRunner) (*QueryAdapter, error) {
	if runner == nil {
		return nil, fmt.Errorf("current query: query dependency is nil")
	}
	return &QueryAdapter{runner: runner}, nil
}

var _ pipelineapi.QueryPort = (*QueryAdapter)(nil)

func (q *QueryAdapter) List(ctx context.Context, query pipelineapi.ListQuery) (pipelineapi.ProjectPage, error) {
	if err := query.Scope.Validate(); err != nil {
		return pipelineapi.ProjectPage{}, err
	}
	limit, err := queryLimit(query.Limit)
	if err != nil {
		return pipelineapi.ProjectPage{}, err
	}
	offset, err := decodeCursor(query.Cursor)
	if err != nil {
		return pipelineapi.ProjectPage{}, err
	}
	if query.Cursor != "" && offset > 0 && query.Limit == 0 {
		return pipelineapi.ProjectPage{}, fmt.Errorf("current query: cursor requires a positive limit")
	}

	projects, err := q.runner.List(ctx, modeling.Query{
		WorkspaceID: query.Scope.WorkspaceID,
		UserID:      query.Scope.UserID,
		Limit:       offset + limit,
	})
	if err != nil {
		return pipelineapi.ProjectPage{}, mapCurrentError(err)
	}
	if offset > len(projects) {
		offset = len(projects)
	}
	end := offset + limit
	if end > len(projects) {
		end = len(projects)
	}
	page := pipelineapi.ProjectPage{Projects: make([]pipelineapi.EngineView, 0, end-offset)}
	for _, project := range projects[offset:end] {
		view := toEngineView(project)
		page.Projects = append(page.Projects, view)
	}
	if end < len(projects) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func (q *QueryAdapter) ReadArtifact(ctx context.Context, query pipelineapi.ArtifactQuery) (pipelineapi.ArtifactContent, error) {
	if err := query.Scope.Validate(); err != nil {
		return pipelineapi.ArtifactContent{}, err
	}
	if query.ProjectID == "" || query.ArtifactID == "" {
		return pipelineapi.ArtifactContent{}, fmt.Errorf("current query: project and artifact are required")
	}
	if query.Offset < 0 {
		return pipelineapi.ArtifactContent{}, fmt.Errorf("current query: offset must not be negative")
	}
	limit, err := queryLimit(int(query.Limit))
	if err != nil {
		return pipelineapi.ArtifactContent{}, err
	}

	project, err := q.runner.Show(ctx, string(query.ProjectID), toModelingScope(query.Scope))
	if err != nil {
		return pipelineapi.ArtifactContent{}, mapCurrentError(err)
	}
	ref, ok := findRef(project, string(query.ArtifactID))
	if !ok {
		return pipelineapi.ArtifactContent{}, mapCurrentError(modeling.ErrNotFound)
	}
	body, err := q.runner.Read(ctx, string(query.ProjectID), ref, toModelingScope(query.Scope))
	if err != nil {
		return pipelineapi.ArtifactContent{}, mapCurrentError(err)
	}
	start := query.Offset
	if start > int64(len(body)) {
		start = int64(len(body))
	}
	end := start + int64(limit)
	if end > int64(len(body)) {
		end = int64(len(body))
	}
	data := append([]byte(nil), body[start:end]...)
	view := refToDescriptor(ref, ref.Stage)
	return pipelineapi.ArtifactContent{
		Artifact: view,
		Data:     data,
		Offset:   start,
		Next:     end,
		EOF:      end >= int64(len(body)),
	}, nil
}

func (q *QueryAdapter) Evidence(ctx context.Context, query pipelineapi.EvidenceQuery) (pipelineapi.ArtifactPage, error) {
	if err := query.Scope.Validate(); err != nil {
		return pipelineapi.ArtifactPage{}, err
	}
	if query.ProjectID == "" {
		return pipelineapi.ArtifactPage{}, fmt.Errorf("current query: project is required")
	}
	limit, err := queryLimit(query.Limit)
	if err != nil {
		return pipelineapi.ArtifactPage{}, err
	}
	offset, err := decodeCursor(query.Cursor)
	if err != nil {
		return pipelineapi.ArtifactPage{}, err
	}
	project, err := q.runner.Show(ctx, string(query.ProjectID), toModelingScope(query.Scope))
	if err != nil {
		return pipelineapi.ArtifactPage{}, mapCurrentError(err)
	}
	refs := append([]modeling.ArtifactRef(nil), project.Evidence...)
	// Project.Evidence is already domain ordered; ref ID is the deterministic
	// tie-breaker for legacy records with equal timestamps/names.
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && evidenceBefore(refs[j], refs[j-1]); j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
	if offset > len(refs) {
		offset = len(refs)
	}
	end := offset + limit
	if end > len(refs) {
		end = len(refs)
	}
	page := pipelineapi.ArtifactPage{Artifacts: make([]pipelineapi.ArtifactDescriptor, 0, end-offset)}
	for _, ref := range refs[offset:end] {
		page.Artifacts = append(page.Artifacts, refToDescriptor(ref, ref.Stage))
	}
	if end < len(refs) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func queryLimit(limit int) (int, error) {
	if limit == 0 {
		return defaultQueryLimit, nil
	}
	if limit < 0 || limit > maxQueryLimit {
		return 0, fmt.Errorf("current query: limit must be between 1 and %d", maxQueryLimit)
	}
	return limit, nil
}

func decodeCursor(cursor string) (int, error) {
	if strings.TrimSpace(cursor) != cursor || cursor == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(cursor)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("current query: invalid cursor")
	}
	return value, nil
}

func findRef(project modeling.Project, id string) (modeling.ArtifactRef, bool) {
	for _, refs := range project.Artifacts {
		for _, ref := range refs {
			if ref.ID == id {
				return ref, true
			}
		}
	}
	for _, ref := range project.Evidence {
		if ref.ID == id {
			return ref, true
		}
	}
	return modeling.ArtifactRef{}, false
}

func evidenceBefore(a, b modeling.ArtifactRef) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.Before(b.Created)
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}
