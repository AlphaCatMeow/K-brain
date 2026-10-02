package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAttachmentDataValidatesAndPreservesFiles(t *testing.T) {
	part := FilePart("application/pdf", "report.pdf", []byte("pdf"))
	mimeType, data, isData, err := AttachmentData(part)
	if err != nil || mimeType != "application/pdf" || data != "cGRm" || !isData {
		t.Fatalf("attachment data = %q %q %v %v", mimeType, data, isData, err)
	}
	if _, _, _, err := AttachmentData(ContentPart{Type: AttachmentFile}); err == nil {
		t.Fatal("missing file URL accepted")
	}
}

func TestResponsesHTTPPreservesFileAndToolImages(t *testing.T) {
	image := ImagePart("png", []byte("png"))
	file := FilePart("application/pdf", "report.pdf", []byte("pdf"))
	call := ToolCall{ID: "call-1", Type: "function"}
	call.Function.Name = "screenshot"
	call.Function.Arguments = `{}`
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"ok\"}]}]}}\n\n")
	}))
	defer srv.Close()
	_, _, err := NewResponses(srv.URL, "key").Stream(t.Context(), Request{Model: "model", Messages: []Message{
		{Role: "user", Parts: []ContentPart{{Type: "text", Text: "inspect"}, image, file}},
		{Role: "assistant", ToolCalls: []ToolCall{call}},
		{Role: "tool", ToolCallID: "call-1", Parts: []ContentPart{image}},
	}}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) < 3 {
		t.Fatalf("input = %#v", body["input"])
	}
	encoded, _ := json.Marshal(input)
	for _, want := range []string{`"type":"input_image"`, `"type":"input_file"`, `"file_data":"data:application/pdf;base64,cGRm"`, `"call_id":"call-1"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("input omitted %s: %s", want, encoded)
		}
	}
}

func TestUnsupportedFileDoesNotReachChatOrAnthropic(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1) }))
	defer srv.Close()
	request := Request{Model: "model", Messages: []Message{{Role: "user", Parts: []ContentPart{FilePart("application/pdf", "x.pdf", []byte("pdf"))}}}}
	for name, client := range map[string]Client{
		"chat":      New(srv.URL, "key"),
		"anthropic": NewAnthropic(srv.URL, "key"),
		"gemini":    NewGemini(srv.URL, "key"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := client.Stream(t.Context(), request, nil, nil, nil); err == nil || !strings.Contains(strings.ToLower(err.Error()), "file") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("unsupported attachment reached provider %d times", requests.Load())
	}
}
