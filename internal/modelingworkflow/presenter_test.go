package modelingworkflow

import (
	"context"
	"strings"
	"testing"

	"github.com/jack-wang-176/qemu-agent/internal/modelingapi"
)

func TestTextPresenterDoesNotRenderArtifactBody(t *testing.T) {
	p := Presentation{
		State: StateWorking, Summary: "Stage completed.",
		Project: &modelingapi.ProjectView{ID: "mp-1", Title: "UART", Status: modelingapi.ProjectRunning, CurrentOperation: "infer", Revision: 2},
		Content: &modelingapi.ArtifactContent{Artifact: modelingapi.ArtifactDescriptor{ID: "a-1", Name: "uart.c", Bytes: 12}, Data: []byte("SECRET_BODY")},
	}
	reply, err := NewTextPresenter().Present(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reply, "SECRET_BODY") || !strings.Contains(reply, "a-1") {
		t.Fatalf("unsafe or incomplete reply: %q", reply)
	}
}
