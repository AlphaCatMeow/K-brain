package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/resources"
)

func TestPromptsAuthenticatedSaveReloadAndMarkdownArguments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LIVEAGENT_HOME", root)
	t.Setenv("K_BRAIN_HOME", "")
	workdir := t.TempDir()
	store, err := resources.OpenPrompts(root)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{prompts: store, token: "prompt-token"}
	request := func(method, path, body, token string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	request(http.MethodPut, "/v1/prompts/templates", `{"templates":[]}`, "wrong", 401)
	request(http.MethodPut, "/v1/prompts/templates", `{"templates":[{"id":"first","name":"First","prompt":"global first","enabled":true},{"id":"second","name":"Second","prompt":"global second","enabled":false}]}`, "prompt-token", 200)
	project := func(prompt, strategy string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"workdir": workdir, "prompt": prompt, "strategy": strategy})
		request(http.MethodPut, "/v1/prompts/project", string(body), "prompt-token", 200)
	}
	project("project policy", "append")
	path := "/v1/prompts?workdir=" + url.QueryEscape(workdir)
	if got := request(http.MethodGet, path, "", "prompt-token", 200)["effectivePrompt"]; got != "global first\n\nproject policy" {
		t.Fatalf("append = %v", got)
	}
	request(http.MethodPatch, "/v1/prompts/templates/second", `{"enabled":true}`, "prompt-token", 200)
	project("project policy", "replace")
	server.prompts, err = resources.OpenPrompts(root)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := request(http.MethodGet, path, "", "prompt-token", 200)
	if reloaded["effectivePrompt"] != "project policy" || reloaded["globalPrompt"] != "global second" {
		t.Fatalf("reloaded = %+v", reloaded)
	}
	project(" \n\t", "replace")
	if got := request(http.MethodGet, path, "", "prompt-token", 200)["effectivePrompt"]; got != "global second" {
		t.Fatalf("blank replace = %v", got)
	}
	canonical, err := resources.CanonicalWorkdir(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if config.Trusted(canonical) {
		t.Fatal("settings save granted filesystem trust")
	}
	if err := os.MkdirAll(filepath.Join(root, "prompts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prompts", "review.md"), []byte("---\ndescription: Review\n---\n[$1] [$2] $@"), 0o600); err != nil {
		t.Fatal(err)
	}
	expanded := request(http.MethodPost, "/v1/prompts/templates/review/expand", `{"args":["two words","'quoted'"]}`, "prompt-token", 200)
	if expanded["text"] != "[two words] ['quoted'] two words 'quoted'" {
		t.Fatalf("argument boundaries lost: %+v", expanded)
	}
}
