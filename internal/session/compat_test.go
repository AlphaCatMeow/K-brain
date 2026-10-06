package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestMixedLayoutReopenAndDeletion(t *testing.T) {
	root := t.TempDir()
	flat, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	flatID, err := flat.Create(root, "old-model", "old-provider")
	if err != nil {
		t.Fatal(err)
	}
	flatPath := flat.TranscriptPath(flatID)
	flat.Close()
	st, err := OpenProjectDir(root)
	if err != nil {
		t.Fatal(err)
	}
	groupedID, err := st.Create(filepath.Join(root, "project"), "old-model", "old-provider")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{flatID, groupedID} {
		if err := st.Save(id, 0, []ai.Message{{Role: "user", Content: "legacy"}}, "old-model", "old-provider"); err != nil {
			t.Fatal(err)
		}
	}
	if st.TranscriptPath(flatID) != flatPath {
		t.Fatal("flat transcript moved")
	}
	st.Close()
	st, err = OpenProjectDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	metas, err := st.Recent(-1)
	if err != nil || len(metas) != 2 {
		t.Fatalf("mixed list: %v %v", metas, err)
	}
	for _, id := range []string{flatID, groupedID} {
		_, messages, err := st.Load(id)
		if err != nil || len(messages) != 1 || messages[0].Content != "legacy" {
			t.Fatalf("load %s: %v %v", id, messages, err)
		}
		if err := st.Delete(id); err != nil {
			t.Fatal(err)
		}
		_, err = st.ImportHistory(id, id, "source", "content", "{}", root, "m", "p", "old", time.Now(), time.Now(), false, false, false, "", nil, nil)
		if !errors.Is(err, ErrHistoryDeleted) {
			t.Fatalf("deleted import: %v", err)
		}
	}
	st.Close()
	st, err = OpenProjectDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = st.ImportHistory(flatID, flatID, "source", "content", "{}", root, "m", "p", "old", time.Now(), time.Now(), false, false, false, "", nil, nil)
	if !errors.Is(err, ErrHistoryDeleted) {
		t.Fatalf("deletion forgotten after restart: %v", err)
	}
	metas, err = st.Recent(-1)
	if err != nil || len(metas) != 0 {
		t.Fatalf("deleted list: %v %v", metas, err)
	}
}

func TestMixedLayoutRejectsAmbiguousIDs(t *testing.T) {
	root := t.TempDir()
	st, err := OpenProjectDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.Create(root, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	original := st.TranscriptPath(id)
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, id), 0700); err != nil {
		t.Fatal(err)
	}
	duplicate := filepath.Join(root, id, "session.jsonl")
	if err := os.WriteFile(duplicate, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Load(id); err == nil {
		t.Fatal("ambiguous ID loaded")
	}
	if err := st.SetTitle(id, "changed"); err == nil {
		t.Fatal("ambiguous ID modified")
	}
	if err := st.Delete(id); err == nil {
		t.Fatal("ambiguous ID deleted")
	}
	for _, path := range []string{original, duplicate} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(data) {
			t.Fatalf("transcript changed: %s", path)
		}
	}
}
