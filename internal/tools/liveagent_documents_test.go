package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveAgentDocumentPreviews(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)
	ts := LiveAgentCatalog()
	writeZIP := func(name string, files map[string]string) {
		t.Helper()
		var b bytes.Buffer
		z := zip.NewWriter(&b)
		for path, content := range files {
			w, err := z.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeZIP("a.docx", map[string]string{"word/document.xml": `<document><body><p><t>Hello document</t></p></body></document>`})
	writeZIP("a.xlsx", map[string]string{
		"xl/sharedStrings.xml":       `<sst><si><t>Shared value</t></si></sst>`,
		"xl/workbook.xml":            `<workbook xmlns:r="relationships"><sheets><sheet name="Budget" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><f>1+1</f><v>2</v></c></row></sheetData></worksheet>`,
	})
	writeZIP("a.zip", map[string]string{"nested/entry.txt": "entry content"})
	for _, tc := range []struct {
		path string
		want []string
	}{{"a.docx", []string{"Hello document"}}, {"a.xlsx", []string{"Sheet: Budget", "A1=Shared value", "B1=2 (formula: 1+1)"}}, {"a.zip", []string{"nested/entry.txt"}}} {
		raw, _ := json.Marshal(map[string]any{"path": tc.path})
		out, err := findToolForTest(ts, "Read").Run(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s missing %q: %s", tc.path, want, out)
			}
		}
	}
}
