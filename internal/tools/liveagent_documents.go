package tools

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func liveagentZIPXML(z *zip.Reader, name string, dst any) error {
	for _, f := range z.File {
		if f.Name == name {
			r, err := f.Open()
			if err != nil {
				return err
			}
			defer r.Close()
			return xml.NewDecoder(io.LimitReader(r, 8<<20)).Decode(dst)
		}
	}
	return fmt.Errorf("document entry %q not found", name)
}
func liveagentSpreadsheet(ctx context.Context, z *zip.Reader, header string) (string, error) {
	type textRun struct {
		Text string `xml:"t"`
	}
	type sharedItem struct {
		Text string    `xml:"t"`
		Runs []textRun `xml:"r"`
	}
	var shared struct {
		Items []sharedItem `xml:"si"`
	}
	_ = liveagentZIPXML(z, "xl/sharedStrings.xml", &shared)
	values := make([]string, len(shared.Items))
	for i, item := range shared.Items {
		values[i] = item.Text
		for _, run := range item.Runs {
			values[i] += run.Text
		}
	}
	var workbook struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	var relationships struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	_ = liveagentZIPXML(z, "xl/workbook.xml", &workbook)
	_ = liveagentZIPXML(z, "xl/_rels/workbook.xml.rels", &relationships)
	labels := map[string]string{}
	for _, sheet := range workbook.Sheets {
		for _, rel := range relationships.Items {
			if sheet.ID == rel.ID {
				target := strings.TrimPrefix(rel.Target, "/")
				if !strings.HasPrefix(target, "xl/") {
					target = "xl/" + target
				}
				labels[target] = sheet.Name
			}
		}
	}
	var b strings.Builder
	b.WriteString(header)
	sheets := 0
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !strings.HasPrefix(f.Name, "xl/worksheets/") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		if sheets >= 8 || b.Len() > maxOutput {
			b.WriteString("\n...truncated...\n")
			break
		}
		sheets++
		var sheet struct {
			Rows []struct {
				Number string `xml:"r,attr"`
				Cells  []struct {
					Ref     string     `xml:"r,attr"`
					Type    string     `xml:"t,attr"`
					Value   string     `xml:"v"`
					Formula string     `xml:"f"`
					Inline  sharedItem `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if err := liveagentZIPXML(z, f.Name, &sheet); err != nil {
			return "", err
		}
		label := labels[f.Name]
		if label == "" {
			label = f.Name
		}
		fmt.Fprintf(&b, "\nSheet: %s (%s)\n", label, f.Name)
		for i, row := range sheet.Rows {
			if i >= 80 {
				b.WriteString("...rows truncated...\n")
				break
			}
			var cells []string
			for j, cell := range row.Cells {
				if j >= 24 {
					break
				}
				value := cell.Value
				if cell.Type == "s" {
					idx, e := strconv.Atoi(value)
					if e == nil && idx >= 0 && idx < len(values) {
						value = values[idx]
					}
				} else if cell.Type == "inlineStr" {
					value = cell.Inline.Text
					for _, r := range cell.Inline.Runs {
						value += r.Text
					}
				}
				if cell.Formula != "" {
					value += " (formula: " + cell.Formula + ")"
				}
				if value != "" {
					cells = append(cells, cell.Ref+"="+value)
				}
			}
			if len(cells) > 0 {
				fmt.Fprintf(&b, "Row %s: %s\n", row.Number, strings.Join(cells, " | "))
			}
		}
	}
	if sheets == 0 {
		return "", fmt.Errorf("Excel workbook contains no worksheets")
	}
	return Truncate(b.String()), nil
}
