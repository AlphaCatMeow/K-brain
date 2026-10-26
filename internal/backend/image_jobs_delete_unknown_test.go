package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// A job whose outcome stayed unknown (for example an Agnes request without a provider
// handle) can be neither cancelled nor reconciled; deleting it is the only way out.
func TestImageJobHTTPDeleteAllowsResultUnknown(t *testing.T) {
	s := imageHTTPFixture(t)
	j, _ := imageHTTPCreateJob(t, s, "delete-unknown")
	err := s.image.Store.Update(imaging.WithScope(context.Background(), imageTestScope()), func(tx *imaging.Transaction) error {
		job, err := tx.ReadJob(imageTestScope(), j.ID)
		if err != nil {
			return err
		}
		job.Revision++
		job.Status = imaging.JobResultUnknown
		for i := range job.Items {
			job.Items[i].Status = imaging.JobResultUnknown
		}
		if err = tx.PutJob(job); err != nil {
			return err
		}
		return tx.AppendEvent(imaging.ImageEvent{JobID: job.ID, Type: imaging.EventJobFinished, Data: imaging.ImageEventData{Status: imaging.JobResultUnknown}})
	})
	if err != nil {
		t.Fatal(err)
	}
	w := imageHTTPRequest(s, "DELETE", "/v1/image/jobs/"+j.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted":true`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w = imageHTTPRequest(s, "GET", "/v1/image/jobs/"+j.ID, nil); w.Code != 404 {
		t.Fatalf("deleted job still readable: %d %s", w.Code, w.Body.String())
	}
}
