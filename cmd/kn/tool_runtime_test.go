package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/browser"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

func TestToolRuntimeInitializationAndDisabledTools(t *testing.T) {
	previous := tools.Browser
	t.Setenv("K_BRAIN_CDP_URL", "original")
	cfg := &config.Config{Browser: config.BrowserConfig{Mode: "headless", CDPURL: "configured", AllowPrivateURLs: true}}
	cleanup, err := initializeToolRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if tools.Browser == nil || !browser.AllowPrivateURLs || os.Getenv("K_BRAIN_CDP_URL") != "configured" {
		t.Fatal("browser configuration not initialized")
	}
	cleanup()
	if tools.Browser != previous || os.Getenv("K_BRAIN_CDP_URL") != "original" {
		t.Fatal("runtime cleanup lost previous state")
	}
	disabled := false
	cfg.Browser.Enabled = &disabled
	cfg.Computer.Enabled = &disabled
	cleanup, err = initializeToolRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	a := agent.New(nil, "fixture", 100, "test", withToolAvailability(cfg))
	for _, tool := range a.AllTools() {
		if tool.Def.Function.Name == "browser_exec" || tool.Def.Function.Name == "computer_exec" {
			t.Fatalf("disabled tool advertised: %s", tool.Def.Function.Name)
		}
	}
}

func TestToolRuntimeBrowserExecRealChrome(t *testing.T) {
	bin := os.Getenv("K_BRAIN_TEST_BROWSER_BIN")
	if bin == "" {
		t.Skip("set K_BRAIN_TEST_BROWSER_BIN to run real browser acceptance")
	}
	t.Setenv("ROD_BROWSER_BIN", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<title>KB browser runtime ready</title><input id="text"><button onclick="document.title=document.getElementById('text').value">Apply</button>`))
	}))
	defer server.Close()
	cleanup, err := initializeToolRuntime(&config.Config{Browser: config.BrowserConfig{Mode: "headless", AllowPrivateURLs: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	a := agent.New(nil, "fixture", 100, "test")
	code := `await goto("` + server.URL + `"); waitFor("#text"); fill("#text","KB tool interaction passed 中文"); js("document.querySelector('button').click()"); print(await js("document.title"))`
	args, _ := json.Marshal(map[string]any{"code": code, "timeout": 30})
	out := tools.Execute(t.Context(), a.AllTools(), "browser_exec", args)
	if !strings.Contains(out, "KB tool interaction passed") || strings.HasPrefix(out, "Error:") {
		t.Fatal(out)
	}
	args, _ = json.Marshal(map[string]any{"code": `print(js("document.title"))`, "timeout": 30})
	out = tools.Execute(t.Context(), a.AllTools(), "browser_exec", args)
	if !strings.Contains(out, "KB tool interaction passed") {
		t.Fatal("browser state lost: " + out)
	}
	args, _ = json.Marshal(map[string]any{"code": `js("document.body.innerHTML='<textarea id=t></textarea><div id=c contenteditable=true></div><input id=r readonly>'"); fill("#t","中文 😀"); fill("#c","editable 中文"); print(js("document.querySelector('#t').value==='中文 😀' && document.querySelector('#c').textContent==='editable 中文' && document.activeElement.id==='c'"))`, "timeout": 30})
	out = tools.Execute(t.Context(), a.AllTools(), "browser_exec", args)
	if strings.HasPrefix(out, "Error:") || !strings.Contains(out, "true") {
		t.Fatalf("fill result: %s", out)
	}
	args, _ = json.Marshal(map[string]any{"code": `fill("#r","forbidden")`, "timeout": 5})
	if out = tools.Execute(t.Context(), a.AllTools(), "browser_exec", args); !strings.Contains(out, "readonly") {
		t.Fatal(out)
	}
}
