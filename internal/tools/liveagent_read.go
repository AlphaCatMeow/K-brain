package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func (s *liveagentState) read(ctx context.Context, a map[string]any) (string, error) {
	path, err := liveagentAuthorizePath(ctx, "Read", laString(a, "path"), "read", false)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	header := fmt.Sprintf("Read: %s\nmtimeMs=%d contentHash=%x\n", path, info.ModTime().UnixMilli(), hash)
	ext := strings.ToLower(filepath.Ext(path))
	if strings.HasPrefix(http.DetectContentType(data), "image/") || ext == ".svg" {
		part, err := liveagentImagePart(data, "")
		if err != nil {
			return "", err
		}
		if !liveagentAttach(ctx, part) {
			return "", errors.New("image attachment unavailable for this model")
		}
		return header + fmt.Sprintf("sizeBytes=%d", len(data)), nil
	}
	if ext == ".ipynb" {
		var notebook struct {
			Cells []struct {
				CellType string            `json:"cell_type"`
				Source   json.RawMessage   `json:"source"`
				Outputs  []json.RawMessage `json:"outputs"`
			} `json:"cells"`
		}
		if err := json.Unmarshal(data, &notebook); err != nil {
			return "", err
		}
		start := min(laInt(a, "cell_start", 1)-1, len(notebook.Cells))
		end := min(start+laInt(a, "cell_limit", 20), len(notebook.Cells))
		var b strings.Builder
		fmt.Fprintf(&b, "%scells=%d-%d/%d\n", header, start+1, end, len(notebook.Cells))
		for i := start; i < end; i++ {
			cell := notebook.Cells[i]
			var lines []string
			var source string
			if json.Unmarshal(cell.Source, &lines) == nil {
				source = strings.Join(lines, "")
			} else {
				_ = json.Unmarshal(cell.Source, &source)
			}
			fmt.Fprintf(&b, "\nCell %d (%s):\n%s\n", i+1, cell.CellType, source)
			for _, output := range cell.Outputs {
				var v map[string]json.RawMessage
				_ = json.Unmarshal(output, &v)
				if text := v["text"]; text != nil {
					var parts []string
					if json.Unmarshal(text, &parts) == nil {
						b.WriteString(strings.Join(parts, ""))
					}
				}
			}
		}
		return Truncate(b.String()), nil
	}
	if ext == ".pdf" {
		binary, err := exec.LookPath("pdftotext")
		if err != nil {
			return "", errors.New("PDF text extraction requires pdftotext (Poppler)")
		}
		start := laInt(a, "page_start", 1)
		end := start + laInt(a, "page_limit", 5) - 1
		out, err := exec.CommandContext(ctx, binary, "-f", fmt.Sprint(start), "-l", fmt.Sprint(end), "-layout", path, "-").CombinedOutput()
		if err != nil {
			return string(out), err
		}
		return header + Truncate(string(out)), nil
	}
	switch ext {
	case ".doc", ".rtf":
		return header + "Legacy Word document recognized. Text extraction requires conversion to .docx.", nil
	case ".xls":
		return header + "Legacy Excel .xls binary recognized. Workbook preview requires conversion to .xlsx.", nil
	case ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz", ".tgz", ".tbz2", ".txz":
		return header + strings.ToUpper(strings.TrimPrefix(ext, ".")) + " archive recognized. Entry previews are supported for ZIP archives.", nil
	}
	if ext == ".zip" || ext == ".docx" || ext == ".xlsx" || ext == ".xlsm" || ext == ".xltx" || ext == ".xltm" || ext == ".pptx" {
		return liveagentArchive(ctx, path, data, header, ext)
	}
	if IsBinary(data) {
		return "", errors.New("unsupported binary document format: " + ext)
	}
	lines := strings.Split(string(data), "\n")
	start := laInt(a, "start_line", 1) - 1
	if start >= len(lines) {
		return "", fmt.Errorf("start_line past end of file (%d lines)", len(lines))
	}
	end := min(start+laInt(a, "limit", 200), len(lines))
	full := start == 0 && end == len(lines)
	previous, ok := s.snapshots[path]
	s.snapshots[path] = liveagentSnapshot{hash, info.ModTime(), full || (ok && previous.Full && previous.Hash == hash && previous.ModTime.Equal(info.ModTime()))}
	viewKey := fmt.Sprintf("%s:%d:%d", path, start, end)
	if previous, ok := s.readViews[viewKey]; ok && previous == hash {
		return header + "This line range is unchanged since the previous Read. Reuse the earlier content.", nil
	}
	s.readViews[viewKey] = hash
	var b strings.Builder
	fmt.Fprintf(&b, "%slines=%d-%d/%d view=%s\n", header, start+1, end, len(lines), map[bool]string{true: "full", false: "partial"}[full])
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
	}
	return Truncate(b.String()), nil
}
func liveagentArchive(ctx context.Context, path string, data []byte, header, ext string) (string, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	if ext == ".xlsx" || ext == ".xlsm" || ext == ".xltx" || ext == ".xltm" {
		return liveagentSpreadsheet(ctx, z, header)
	}
	var b strings.Builder
	b.WriteString(header)
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if b.Len() > maxOutput {
			b.WriteString("\n...truncated...\n")
			break
		}
		if ext == ".zip" {
			fmt.Fprintf(&b, "%s (%d bytes)\n", f.Name, f.UncompressedSize64)
			continue
		}
		if !(strings.HasPrefix(f.Name, "word/") || strings.HasPrefix(f.Name, "xl/") || strings.HasPrefix(f.Name, "ppt/slides/")) || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return "", err
		}
		d := xml.NewDecoder(io.LimitReader(r, 8<<20))
		fmt.Fprintf(&b, "\n%s:\n", f.Name)
		for {
			token, e := d.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				_ = r.Close()
				return "", e
			}
			if text, ok := token.(xml.CharData); ok {
				b.Write(text)
				b.WriteByte(' ')
			}
			if b.Len() > maxOutput {
				break
			}
		}
		_ = r.Close()
	}
	return Truncate(b.String()), nil
}
func liveagentAttach(ctx context.Context, part ai.ContentPart) bool {
	a, _ := ctx.Value(attachmentKey{}).(*attachments)
	if a == nil || ctx.Err() != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || !a.enabled {
		return false
	}
	a.parts = append(a.parts, part)
	return true
}
func liveagentImagePart(data []byte, mime string) (ai.ContentPart, error) {
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	if strings.Contains(string(data[:min(len(data), 512)]), "<svg") {
		mime = "image/svg+xml"
	}
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/svg+xml":
	default:
		return ai.ContentPart{}, errors.New("not a supported image")
	}
	return ai.ImagePart(strings.TrimPrefix(mime, "image/"), data), nil
}
func liveagentImage(ctx context.Context, a map[string]any) (string, error) {
	type source struct{ kind, value string }
	var sources []source
	for _, pair := range [][3]string{{"source", "sources", "auto"}, {"path", "paths", "path"}, {"url", "urls", "url"}, {"base64", "base64s", "base64"}} {
		if many, ok := a[pair[1]].([]any); ok {
			for _, v := range many {
				sources = append(sources, source{pair[2], v.(string)})
			}
		} else if v := laString(a, pair[0]); v != "" {
			sources = append(sources, source{pair[2], v})
		}
	}
	if len(sources) == 0 || len(sources) > 12 {
		return "", errors.New("Image requires between 1 and 12 sources")
	}
	var parts []ai.ContentPart
	var descriptions []string
	for _, s := range sources {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		v := strings.TrimSpace(s.value)
		if v == "" {
			return "", errors.New("Image source must be non-empty")
		}
		if s.kind == "auto" {
			switch {
			case strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://"):
				s.kind = "url"
			case strings.HasPrefix(v, "data:image/") || strings.HasPrefix(v, "iVBOR") || strings.HasPrefix(v, "/9j/"):
				s.kind = "base64"
			case strings.HasPrefix(v, "<svg") || strings.HasPrefix(v, "<?xml"):
				s.kind = "svg"
			default:
				s.kind = "path"
			}
		}
		var part ai.ContentPart
		if s.kind == "url" {
			u, err := url.Parse(v)
			if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return "", errors.New("Image.url must be an HTTP(S) URL without credentials")
			}
			if deny := checkGate(ctx, "Image", v); deny != "" {
				return "", errors.New(deny)
			}
			part = ai.ImagePart("png", nil)
			part.ImageURL.URL = v
			part.MimeType = ""
		} else {
			if s.kind != "path" {
				if denied := checkGate(ctx, "Image", "inline image"); denied != "" {
					return "", errors.New(denied)
				}
			}
			var data []byte
			var err error
			mime := laString(a, "mimeType")
			switch s.kind {
			case "path":
				path, e := liveagentAuthorizePath(ctx, "Image", v, "read", false)
				if e != nil {
					return "", e
				}
				data, err = os.ReadFile(path)
			case "base64":
				if strings.HasPrefix(v, "data:") {
					prefix, body, ok := strings.Cut(v, ",")
					if !ok || !strings.HasSuffix(prefix, ";base64") {
						return "", errors.New("Image data URL must use base64")
					}
					mime = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
					v = body
				}
				data, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(v), ""))
			case "svg":
				data = []byte(v)
			}
			if err != nil {
				return "", err
			}
			part, err = liveagentImagePart(data, mime)
			if err != nil {
				return "", err
			}
		}
		parts = append(parts, part)
		label := s.value
		if s.kind == "base64" || s.kind == "svg" {
			label = s.kind
		}
		descriptions = append(descriptions, "Display image: "+label)
	}
	for _, p := range parts {
		if !liveagentAttach(ctx, p) {
			return "", errors.New("image attachment unavailable for this model")
		}
	}
	return strings.Join(descriptions, "\n"), nil
}
