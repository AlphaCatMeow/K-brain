package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestImageProjectHTTPCRUDRevisionAndScope(t *testing.T) {
	s := imageHTTPFixture(t)
	w := imageHTTPRequest(s, "POST", "/v1/image/projects", strings.NewReader(`{"title":"first","workspaceRef":"opaque/path"}`))
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var result struct{ Project imaging.ImageProject }
	if json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Body.String())
	}
	p := result.Project
	w = imageHTTPRequest(s, "PATCH", "/v1/image/projects/"+p.ID, strings.NewReader(`{"title":"next","expectedRevision":1}`))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = imageHTTPRequest(s, "PATCH", "/v1/image/projects/"+p.ID, strings.NewReader(`{"title":"stale","expectedRevision":1}`))
	imageAssertError(t, w, 409, imaging.CodeRevisionConflict)
	w = imageHTTPRequest(s, "PATCH", "/v1/image/projects/"+p.ID, strings.NewReader(`{"title":"missing revision"}`))
	imageAssertError(t, w, 422, imaging.CodeInvalidInput)
	other, err := s.image.Store.CreateProject(context.Background(), imaging.Scope{OwnerID: "other", BackendID: "local"}, "private", "")
	if err != nil {
		t.Fatal(err)
	}
	w = imageHTTPRequest(s, "GET", "/v1/image/projects/"+other.ID, nil)
	imageAssertError(t, w, 404, imaging.CodeNotFound)
	w = imageHTTPRequest(s, "GET", "/v1/image/projects?limit=1", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), other.ID) || !strings.Contains(w.Body.String(), p.ID) {
		t.Fatal(w.Body.String())
	}
	w = imageHTTPRequest(s, "DELETE", "/v1/image/projects/"+p.ID, strings.NewReader(`{"expectedRevision":1}`))
	imageAssertError(t, w, 409, imaging.CodeRevisionConflict)
	w = imageHTTPRequest(s, "DELETE", "/v1/image/projects/"+p.ID, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = imageHTTPRequest(s, "GET", "/v1/image/projects/"+p.ID, nil)
	imageAssertError(t, w, 404, imaging.CodeNotFound)
	j, _ := imageHTTPCreateJob(t, s, "busy-key")
	w = imageHTTPRequest(s, "DELETE", fmt.Sprintf("/v1/image/projects/%s", j.ProjectID), nil)
	imageAssertError(t, w, 409, imaging.CodeProjectBusy)
}
