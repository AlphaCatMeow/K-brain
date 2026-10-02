package protocol

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestCanonicalAttachmentsRoundTripAcrossModelSwitch(t *testing.T) {
	image := "data:image/png;base64,cG5n"
	file := "data:application/pdf;base64,cGRm"
	original := Message{Role: RoleUser, Content: []ContentBlock{
		{Type: ContentText, Text: "inspect"},
		{Type: ContentImage, ImageURL: image, MimeType: "image/png"},
		{Type: ContentFile, FileURL: file, Filename: "report.pdf", MimeType: "application/pdf"},
	}}
	if err := original.Validate(); err != nil {
		t.Fatal(err)
	}
	converted, err := original.ToAIMessage()
	if err != nil {
		t.Fatal(err)
	}
	replayed := FromAIMessage(converted)
	if !reflect.DeepEqual(replayed.Content, original.Content) {
		t.Fatalf("attachments changed across conversion: got=%+v want=%+v", replayed.Content, original.Content)
	}
	tool := Message{Role: RoleTool, ToolCallID: "screenshot-1", Name: "screenshot", Content: []ContentBlock{{Type: ContentImage, ImageURL: image, MimeType: "image/png"}}}
	convertedTool, err := tool.ToAIMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := FromAIMessage(convertedTool); !reflect.DeepEqual(got.Content, tool.Content) {
		t.Fatalf("tool-result image changed across conversion: got=%+v want=%+v", got.Content, tool.Content)
	}
	wire, err := json.Marshal(replayed)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(wire, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Content, original.Content) {
		t.Fatalf("attachments changed across JSON persistence: got=%+v want=%+v", restored.Content, original.Content)
	}
}

func TestCanonicalAttachmentValidationRejectsLossyShape(t *testing.T) {
	for name, block := range map[string]ContentBlock{
		"missing file mime": {Type: ContentFile, FileURL: "https://example.test/report.pdf"},
		"image file fields": {Type: ContentImage, ImageURL: "https://example.test/image.png", FileURL: "https://example.test/report.pdf"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := (Message{Role: RoleUser, Content: []ContentBlock{block}}).Validate(); err == nil {
				t.Fatal("invalid attachment accepted")
			}
		})
	}
	if _, err := (Message{Role: RoleUser, Content: []ContentBlock{{Type: ContentFile, FileURL: "https://example.test/report.pdf", MimeType: "application/pdf"}}}).ToAIMessage(); err != nil {
		t.Fatal(err)
	}
	if got := ai.FilePart("application/pdf", "report.pdf", []byte("pdf")); got.FileURL == nil {
		t.Fatal("file constructor did not preserve data")
	}
}
