package modelingworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const maxPresentedReplyBytes = 16 * 1024

// TextPresenter renders only bounded public workflow data. Artifact content is
// deliberately represented by its descriptor so it cannot enter session text.
type TextPresenter struct{}

func NewTextPresenter() TextPresenter { return TextPresenter{} }

func (TextPresenter) Present(ctx context.Context, p Presentation) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validatePresentation(p); err != nil {
		return "", err
	}
	var lines []string
	if summary := strings.TrimSpace(p.Summary); summary != "" {
		lines = append(lines, summary)
	}
	if p.Project != nil {
		lines = append(lines, fmt.Sprintf(
			"Project %s: %s (status: %s, operation: %s, revision: %d).",
			p.Project.ID, p.Project.Title, p.Project.Status,
			p.Project.CurrentOperation, p.Project.Revision,
		))
	}
	if len(p.Artifacts) > 0 {
		lines = append(lines, fmt.Sprintf("Artifacts produced: %d.", len(p.Artifacts)))
	}
	if len(p.Evidence) > 0 {
		lines = append(lines, fmt.Sprintf("Evidence items: %d.", len(p.Evidence)))
	}
	if p.Content != nil {
		lines = append(lines, fmt.Sprintf(
			"Artifact %s (%s, %d bytes) is available through bounded artifact reading.",
			p.Content.Artifact.ID, p.Content.Artifact.Name, p.Content.Artifact.Bytes,
		))
	}
	if p.Failure != nil {
		lines = append(lines, p.Failure.Message)
	}
	if question := strings.TrimSpace(p.Question); question != "" {
		lines = append(lines, question)
	}
	reply := strings.Join(lines, "\n")
	if strings.TrimSpace(reply) == "" {
		return "", errors.New("modelingworkflow: presenter produced an empty reply")
	}
	if len(reply) > maxPresentedReplyBytes {
		return "", errors.New("modelingworkflow: presented reply exceeds the byte limit")
	}
	return reply, nil
}

var _ Presenter = TextPresenter{}
