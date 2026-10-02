package agent

import (
	"context"
	"encoding/json"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/skills"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func skillsManagerTool() tools.Tool {
	return tools.Tool{
		Def: ai.NewTool("SkillsManager", "Read and manage Skills in LiveAgent's fixed user Skills directory. Use read/list/install/create/validate/package/delete/clawhub_search/clawhub_install and install job actions; use Read/List/Glob/Grep/Write/Edit/Delete with skill:// paths for files inside enabled Skills.", `{"type":"object","required":["action"],"properties":{"action":{"type":"string","enum":["read","list","install","create","validate","package","delete","clawhub_search","clawhub_install","install_start","install_status","install_cancel","scan_external","scan_external_mcp","scan_mcp_file"]},"path":{"type":"string","description":"Skill entry path for read, such as skill://<baseDir>/SKILL.md or <baseDir>/SKILL.md."},"offset":{"type":"number","minimum":0,"description":"0-based starting line for read."},"length":{"type":"number","minimum":0,"description":"Number of lines for read; defaults to 200."},"source":{"type":"string","description":"Local directory, .zip/.skill archive, HTTP(S) source, or GitHub source for install."},"query":{"type":"string","description":"Search text for clawhub_search."},"sort":{"type":"string","enum":["downloads","stars","installs","updated","newest"]},"limit":{"type":"number","minimum":1,"maximum":20},"cursor":{"type":"string"},"name":{"type":"string","description":"Skill name for create, validate, package, delete, or install rename."},"description":{"type":"string"},"body":{"type":"string"},"files":{"type":"array","items":{"type":"object","required":["path","content"],"properties":{"path":{"type":"string"},"content":{"type":"string"}},"additionalProperties":false}},"conflict":{"type":"string","enum":["backup","fail","overwrite"]},"method":{"type":"string","enum":["auto","download","git"]},"ref":{"type":"string"},"jobId":{"type":"string"},"owner":{"type":"string","description":"ClawHub owner handle for clawhub_install."},"ownerHandle":{"type":"string","description":"Compatibility alias for the ClawHub owner handle."},"slug":{"type":"string"},"version":{"type":"string"}},"additionalProperties":false}`),
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			manager, err := skills.NewManager()
			if err != nil {
				return "", err
			}
			var input map[string]any
			if err := json.Unmarshal(args, &input); err != nil {
				return "", err
			}
			if _, ok := input["ownerHandle"]; !ok {
				if owner, ok := input["owner"].(string); ok && owner != "" {
					input["ownerHandle"] = owner
				}
			}
			out, err := manager.Manage(ctx, input)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(out)
			return string(b), err
		},
	}
}
