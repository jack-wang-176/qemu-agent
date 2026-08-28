package modelingworkflow

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

func TestSelectReviewArtifactRequiresUnambiguousDiff(t *testing.T) {
	view := artifactProject("plan", artifactDescriptor("diff", "plan", []byte("patch")))
	selected, ok := selectReviewArtifact(view)
	if !ok || selected.Kind != "diff" {
		t.Fatalf("selectReviewArtifact() = %#v, %v", selected, ok)
	}
	view.Artifacts = append(view.Artifacts, artifactDescriptor("diff", "plan", []byte("other")))
	if _, ok := selectReviewArtifact(view); ok {
		t.Fatal("selectReviewArtifact() accepted ambiguous diffs")
	}
}

func TestValidateBoundedArtifactVerifiesFullDigest(t *testing.T) {
	body := []byte("patch")
	descriptor := artifactDescriptor("diff", "plan", body)
	content := modelingapi.ArtifactContent{Artifact: descriptor, Data: body, Offset: 0, Next: int64(len(body)), EOF: true}
	if err := validateBoundedArtifact(content, descriptor.ID, 64); err != nil {
		t.Fatalf("validateBoundedArtifact() error = %v", err)
	}
	content.Data = []byte("wrong")
	if err := validateBoundedArtifact(content, descriptor.ID, 64); err == nil {
		t.Fatal("validateBoundedArtifact() accepted content with a mismatched digest")
	}
}

func TestValidateBoundedArtifactRejectsInvalidReadProjection(t *testing.T) {
	body := []byte("patch")
	descriptor := artifactDescriptor("diff", "plan", body)
	valid := modelingapi.ArtifactContent{Artifact: descriptor, Data: body, Offset: 0, Next: int64(len(body)), EOF: true}
	tests := map[string]modelingapi.ArtifactContent{
		"different artifact": func() modelingapi.ArtifactContent {
			out := valid
			out.Artifact = artifactDescriptor("diff", "plan", []byte("other"))
			return out
		}(),
		"nonzero offset": func() modelingapi.ArtifactContent {
			out := valid
			out.Offset = 1
			return out
		}(),
		"inconsistent eof": func() modelingapi.ArtifactContent {
			out := valid
			out.EOF = false
			return out
		}(),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateBoundedArtifact(content, descriptor.ID, 4); err == nil {
				t.Fatal("validateBoundedArtifact() error = nil")
			}
		})
	}
}

func artifactProject(operation modelingapi.OperationName, artifacts ...modelingapi.ArtifactDescriptor) modelingapi.ProjectView {
	return modelingapi.ProjectView{ID: "mp-0123456789abcdef", Title: "UART model", Revision: 1,
		Status: modelingapi.ProjectBlocked, CurrentOperation: operation, Artifacts: artifacts,
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)}
}

func artifactDescriptor(kind string, operation modelingapi.OperationName, body []byte) modelingapi.ArtifactDescriptor {
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	return modelingapi.ArtifactDescriptor{ID: modelingapi.ArtifactID(digest[:16]), Operation: operation,
		Name: "review.diff", Kind: kind, Bytes: int64(len(body)), Digest: digest, CreatedAt: time.Unix(1, 0)}
}
