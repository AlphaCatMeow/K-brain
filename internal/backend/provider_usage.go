package backend

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/dop251/goja"
)

const (
	usageMaxScriptBytes   = 64 << 10
	usageMaxResponseBytes = 1 << 20
	usageMaxBodyBytes     = 64 << 10
	usageMaxHeaders       = 64
	usageMaxEntries       = 16
	usageScriptTimeout    = 100 * time.Millisecond
	usageDefaultTimeout   = 10 * time.Second
)

type UsageData struct {
	PlanName       string   `json:"planName,omitempty"`
	Extra          string   `json:"extra,omitempty"`
	IsValid        *bool    `json:"isValid,omitempty"`
	InvalidMessage string   `json:"invalidMessage,omitempty"`
	Total          *float64 `json:"total,omitempty"`
	Used           *float64 `json:"used,omitempty"`
	Remaining      *float64 `json:"remaining,omitempty"`
	Unit           string   `json:"unit,omitempty"`
}

type ProviderUsageResult struct {
	Data      []UsageData `json:"data"`
	QueriedAt *int64      `json:"queriedAt"`
	Error     *string     `json:"error"`
	IsStale   bool        `json:"isStale"`
}

type usageConfig struct {
	Enabled                   bool              `json:"enabled"`
	Mode                      string            `json:"mode"`
	Script                    string            `json:"script"`
	Scripts                   map[string]string `json:"scripts"`
	BaseURL                   string            `json:"baseUrl"`
	APIKey                    string            `json:"apiKey"`
	APIKeyConfigured          bool              `json:"apiKeyConfigured"`
	AccessToken               string            `json:"accessToken"`
	AccessTokenConfigured     bool              `json:"accessTokenConfigured"`
	UserID                    string            `json:"userId"`
	AccessKeyID               string            `json:"accessKeyId"`
	SecretAccessKey           string            `json:"secretAccessKey"`
	SecretAccessKeyConfigured bool              `json:"secretAccessKeyConfigured"`
	CodingPlanProvider        string            `json:"codingPlanProvider"`
	TeamOrganizationID        string            `json:"teamOrganizationId"`
	TeamProjectID             string            `json:"teamProjectId"`
	TimeoutSecs               float64           `json:"timeoutSecs"`
}

var generalUsageScript = `({request:{url:"{{baseUrl}}/user/balance",method:"GET",headers:{"Authorization":"Bearer {{apiKey}}","User-Agent":"LiveAgent/1.0"}},extractor:function(response){return {isValid:response.is_active||true,remaining:response.balance,unit:"USD"};}})`
var newAPIUsageScript = `({request:{url:"{{baseUrl}}/api/user/self",method:"GET",headers:{"Content-Type":"application/json","Authorization":"Bearer {{accessToken}}","User-Agent":"LiveAgent/1.0","New-Api-User":"{{userId}}"}},extractor:function(response){if(response.success&&response.data){return {planName:response.data.group||"Balance",remaining:response.data.quota/500000,used:response.data.used_quota/500000,total:(response.data.quota+response.data.used_quota)/500000,unit:"USD"};}return {isValid:false,invalidMessage:response.message||"NewAPI usage query failed"};}})`

type usageFailure struct {
	message   string
	transient bool
}
type usageCacheEntry struct {
	identity [32]byte
	result   ProviderUsageResult
}
type ProviderUsageService struct {
	settings *SettingsStore
	mu       sync.Mutex
	cache    map[string]usageCacheEntry
	client   *http.Client
}

func NewProviderUsageService(settings *SettingsStore) *ProviderUsageService {
	return &ProviderUsageService{settings: settings, cache: make(map[string]usageCacheEntry), client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *ProviderUsageService) Query(ctx context.Context, providerID string, refresh bool) ProviderUsageResult {
	p, ok := s.provider(providerID)
	if !ok {
		return usageError("provider not found")
	}
	cfg := usageMetadata(p)
	if !cfg.Enabled {
		return usageError("usage query is disabled")
	}
	key, err := p.ResolveKeyContext(ctx)
	if err != nil {
		return usageError("provider credentials are unavailable")
	}
	identity := usageIdentity(providerID, p, cfg, key)
	if !refresh {
		s.mu.Lock()
		cached, found := s.cache[providerID]
		s.mu.Unlock()
		if found && cached.identity == identity && len(cached.result.Data) > 0 {
			return cached.result
		}
	}
	data, failure := s.execute(ctx, p, cfg, key)
	if failure == nil && len(data) > 0 {
		r := usageSuccess(data)
		s.mu.Lock()
		s.cache[providerID] = usageCacheEntry{identity: identity, result: r}
		s.mu.Unlock()
		return r
	}
	if failure == nil {
		failure = &usageFailure{message: "usage query returned no entries"}
	}
	if failure.transient {
		s.mu.Lock()
		cached, found := s.cache[providerID]
		if found && cached.identity == identity && len(cached.result.Data) > 0 {
			message := failure.message
			cached.result.Error = &message
			cached.result.IsStale = true
			s.cache[providerID] = cached
			s.mu.Unlock()
			return cached.result
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	delete(s.cache, providerID)
	s.mu.Unlock()
	return usageError(failure.message)
}

func (s *ProviderUsageService) Test(ctx context.Context, providerID string, draft usageConfig) ProviderUsageResult {
	p, ok := s.provider(providerID)
	if !ok {
		return usageError("provider not found")
	}
	stored := usageMetadata(p)
	if draft.APIKey == "" && draft.APIKeyConfigured {
		draft.APIKey = stored.APIKey
	}
	if draft.AccessToken == "" && draft.AccessTokenConfigured {
		draft.AccessToken = stored.AccessToken
	}
	if draft.SecretAccessKey == "" && draft.SecretAccessKeyConfigured {
		draft.SecretAccessKey = stored.SecretAccessKey
	}
	key, err := p.ResolveKeyContext(ctx)
	if err != nil {
		return usageError("provider credentials are unavailable")
	}
	if draft.APIKey == "" {
		draft.APIKey = key
	}
	if draft.AccessToken == "" {
		draft.AccessToken = draft.APIKey
	}
	draft.Enabled = true
	data, failure := s.execute(ctx, p, draft, key)
	if failure != nil {
		return usageError(failure.message)
	}
	if len(data) == 0 {
		return usageError("usage query returned no entries")
	}
	return usageSuccess(data)
}

func (s *ProviderUsageService) ProviderExists(id string) bool {
	_, ok := s.provider(id)
	return ok
}

func (s *ProviderUsageService) provider(id string) (config.Provider, bool) {
	if s.settings == nil {
		return config.Provider{}, false
	}
	p, ok := s.settings.Snapshot().Providers[id]
	return p, ok
}

// usageMetadata resolves the provider usage block. Settings writes it to
// Provider.UsageQuery, while older configurations carried it inside
// Metadata["usageQuery"]; read the canonical field first and keep the legacy
// location working.
func usageMetadata(p config.Provider) usageConfig {
	var cfg usageConfig
	var raw any = p.UsageQuery
	if len(p.UsageQuery) == 0 {
		raw = p.Metadata["usageQuery"]
	}
	if raw != nil {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.Scripts == nil {
		cfg.Scripts = map[string]string{}
	}
	return cfg
}
func usageIdentity(id string, p config.Provider, c usageConfig, key string) [32]byte {
	b, _ := json.Marshal([]any{id, p.Type, p.API, p.BaseURL, key, c})
	return sha256.Sum256(b)
}
func usageSuccess(data []UsageData) ProviderUsageResult {
	now := time.Now().UnixMilli()
	return ProviderUsageResult{Data: data, QueriedAt: &now}
}
func usageError(message string) ProviderUsageResult {
	return ProviderUsageResult{Data: []UsageData{}, Error: &message}
}

func (s *ProviderUsageService) execute(ctx context.Context, p config.Provider, c usageConfig, providerKey string) ([]UsageData, *usageFailure) {
	timeout := usageDefaultTimeout
	if c.TimeoutSecs > 0 && !math.IsNaN(c.TimeoutSecs) && !math.IsInf(c.TimeoutSecs, 0) {
		seconds := math.Round(c.TimeoutSecs)
		if seconds < 2 {
			seconds = 2
		}
		if seconds > 30 {
			seconds = 30
		}
		timeout = time.Duration(seconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "" {
		return nil, &usageFailure{message: "unsupported usage query mode"}
	}
	if c.BaseURL == "" {
		c.BaseURL = p.BaseURL
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.APIKey == "" {
		c.APIKey = providerKey
	}
	if c.AccessToken == "" {
		c.AccessToken = c.APIKey
	}
	switch mode {
	case "balance":
		return s.executeBalance(ctx, p, c, providerKey)
	case "coding-plan":
		return s.executeCodingPlan(ctx, p, c, providerKey)
	case "general", "newapi", "custom":
		script := c.Script
		if script == "" {
			script = c.Scripts[mode]
		}
		if script == "" && mode == "general" {
			script = generalUsageScript
		}
		if script == "" && mode == "newapi" {
			script = newAPIUsageScript
		}
		if script == "" {
			return nil, &usageFailure{message: "custom usage script is empty"}
		}
		return s.executeScript(ctx, c, script, mode != "custom")
	default:
		return nil, &usageFailure{message: "unsupported usage query mode"}
	}
}

func usageURL(raw string) (*url.URL, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("usage query URL must be HTTP(S) without credentials")
	}
	return u, nil
}
func (s *ProviderUsageService) executeBalance(ctx context.Context, p config.Provider, c usageConfig, providerKey string) ([]UsageData, *usageFailure) {
	u, e := usageURL(p.BaseURL)
	if e != nil {
		return nil, &usageFailure{message: e.Error()}
	}
	if u.Scheme != "https" {
		return nil, &usageFailure{message: "built-in usage adapters require HTTPS"}
	}
	host := strings.ToLower(u.Hostname())
	var endpoint, adapter string
	switch host {
	case "api.deepseek.com":
		endpoint, adapter = "https://api.deepseek.com/user/balance", "deepseek"
	case "api.stepfun.com":
		endpoint, adapter = "https://api.stepfun.com/v1/accounts", "stepfun-cny"
	case "api.stepfun.ai":
		endpoint, adapter = "https://api.stepfun.ai/v1/accounts", "stepfun"
	case "api.siliconflow.cn":
		endpoint, adapter = "https://api.siliconflow.cn/v1/user/info", "siliconflow-cny"
	case "api.siliconflow.com":
		endpoint, adapter = "https://api.siliconflow.com/v1/user/info", "siliconflow"
	case "openrouter.ai":
		endpoint, adapter = "https://openrouter.ai/api/v1/credits", "openrouter"
	case "api.novita.ai":
		endpoint, adapter = "https://api.novita.ai/v3/user/balance", "novita"
	default:
		return nil, &usageFailure{message: "no balance adapter matches this provider"}
	}
	body, status, f := s.httpJSON(ctx, endpoint, "GET", map[string]string{"Authorization": "Bearer " + c.APIKey, "Accept": "application/json"}, "")
	if f != nil {
		return nil, f
	}
	if status == 401 || status == 403 {
		return nil, &usageFailure{message: "usage query authentication failed"}
	}
	if status < 200 || status >= 300 {
		return nil, usageHTTPFailure(status)
	}
	return parseBalance(adapter, body)
}
func (s *ProviderUsageService) executeCodingPlan(ctx context.Context, p config.Provider, c usageConfig, providerKey string) ([]UsageData, *usageFailure) {
	plan := strings.ToLower(strings.TrimSpace(c.CodingPlanProvider))
	if plan == "" {
		plan = detectUsagePlan(c.BaseURL)
	}
	var endpoint, adapter string
	switch plan {
	case "kimi":
		endpoint, adapter = "https://api.kimi.com/coding/v1/usages", "kimi"
	case "zhipu":
		endpoint, adapter = "https://open.bigmodel.cn/api/monitor/usage/quota/limit", "zhipu"
	case "zhipu_team":
		if c.TeamOrganizationID == "" || c.TeamProjectID == "" {
			return nil, &usageFailure{message: "Zhipu team plan needs organization ID and project ID"}
		}
		endpoint, adapter = "https://open.bigmodel.cn/api/monitor/usage/quota/limit?type=2", "zhipu"
	case "minimax":
		endpoint, adapter = "https://api.minimaxi.com/v1/api/openplatform/coding_plan/remains", "minimax"
	case "zenmux":
		endpoint, adapter = strings.TrimRight(c.BaseURL, "/"), "zenmux"
	default:
		return nil, &usageFailure{message: "no coding plan adapter matches this provider"}
	}
	headers := map[string]string{"Authorization": "Bearer " + c.APIKey, "Accept": "application/json"}
	if adapter == "zhipu" {
		headers["Authorization"] = c.APIKey
	}
	if plan == "zhipu_team" {
		headers["bigmodel-organization"] = c.TeamOrganizationID
		headers["bigmodel-project"] = c.TeamProjectID
	}
	body, status, f := s.httpJSON(ctx, endpoint, "GET", headers, "")
	if f != nil {
		return nil, f
	}
	if status == 401 || status == 403 {
		return nil, &usageFailure{message: "usage query authentication failed"}
	}
	if status < 200 || status >= 300 {
		return nil, usageHTTPFailure(status)
	}
	return parseCoding(adapter, body)
}
func detectUsagePlan(raw string) string {
	switch {
	case strings.Contains(raw, "api.kimi.com/coding"):
		return "kimi"
	case strings.Contains(raw, "bigmodel.cn"), strings.Contains(raw, "api.z.ai"):
		return "zhipu"
	case strings.Contains(raw, "minimax"):
		return "minimax"
	case strings.Contains(raw, "zenmux"):
		return "zenmux"
	}
	return ""
}
func usageHTTPFailure(status int) *usageFailure {
	return &usageFailure{message: fmt.Sprintf("usage query failed with HTTP %d", status), transient: status == 408 || status == 425 || status == 429 || status >= 500}
}

func parseBalance(adapter string, v map[string]any) ([]UsageData, *usageFailure) {
	num := func(x any) (float64, bool) {
		switch n := x.(type) {
		case float64:
			return n, math.IsNaN(n) == false && math.IsInf(n, 0) == false
		case json.Number:
			f, e := n.Float64()
			return f, e == nil
		case string:
			f, e := parseFinite(n)
			return f, e == nil
		}
		return 0, false
	}
	balance := func(name string, x float64, unit string) []UsageData {
		return []UsageData{{PlanName: name, Remaining: &x, Unit: unit}}
	}
	switch adapter {
	case "deepseek":
		infos, _ := v["balance_infos"].([]any)
		out := []UsageData{}
		for _, raw := range infos {
			m, _ := raw.(map[string]any)
			x, ok := num(m["total_balance"])
			if !ok {
				continue
			}
			unit, _ := m["currency"].(string)
			d := UsageData{PlanName: "DeepSeek", Remaining: &x, Unit: unit}
			if v["is_available"] == false {
				valid := false
				d.IsValid = &valid
				d.InvalidMessage = "Insufficient balance"
			}
			out = append(out, d)
		}
		return out, nil
	case "stepfun-cny":
		x, ok := num(v["balance"])
		if !ok {
			return nil, &usageFailure{message: "usage response is missing balance"}
		}
		return balance("StepFun", x, "CNY"), nil
	case "stepfun":
		x, ok := num(v["balance"])
		if !ok {
			return nil, &usageFailure{message: "usage response is missing balance"}
		}
		return balance("StepFun", x, "USD"), nil
	case "siliconflow-cny", "siliconflow":
		m, _ := v["data"].(map[string]any)
		x, ok := num(m["totalBalance"])
		if !ok {
			return nil, &usageFailure{message: "usage response is missing totalBalance"}
		}
		unit := "USD"
		if adapter == "siliconflow-cny" {
			unit = "CNY"
		}
		return balance("SiliconFlow", x, unit), nil
	case "openrouter":
		m, _ := v["data"].(map[string]any)
		if m == nil {
			m = v
		}
		total, ok := num(m["total_credits"])
		if !ok {
			return nil, &usageFailure{message: "usage response is missing total_credits"}
		}
		used, ok := num(m["total_usage"])
		if !ok {
			return nil, &usageFailure{message: "usage response is missing total_usage"}
		}
		remaining := total - used
		valid := remaining > 0
		return []UsageData{{PlanName: "OpenRouter", Total: &total, Used: &used, Remaining: &remaining, Unit: "USD", IsValid: &valid, InvalidMessage: "No credits remaining"}}, nil
	case "novita":
		x, ok := num(v["availableBalance"])
		if !ok {
			return nil, &usageFailure{message: "usage response is missing availableBalance"}
		}
		x /= 10000
		valid := x > 0
		return []UsageData{{PlanName: "Novita", Remaining: &x, Unit: "USD", IsValid: &valid, InvalidMessage: "No balance remaining"}}, nil
	}
	return nil, &usageFailure{message: "unsupported balance adapter"}
}
func parseFinite(s string) (float64, error) {
	var x float64
	_, e := fmt.Sscan(strings.TrimSpace(s), &x)
	if e != nil || math.IsNaN(x) || math.IsInf(x, 0) {
		return 0, errors.New("not finite")
	}
	return x, nil
}

func parseCoding(adapter string, v map[string]any) ([]UsageData, *usageFailure) {
	out := []UsageData{}
	pct := func(name string, remaining float64) UsageData {
		total := 100.0
		used := 100 - remaining
		return UsageData{PlanName: name, Remaining: &remaining, Total: &total, Used: &used, Unit: "%"}
	}
	num := func(m map[string]any, k string) (float64, bool) {
		x, ok := m[k].(float64)
		return x, ok && !math.IsNaN(x) && !math.IsInf(x, 0)
	}
	switch adapter {
	case "kimi":
		if ls, ok := v["limits"].([]any); ok {
			for _, r := range ls {
				m, _ := r.(map[string]any)
				d, _ := m["detail"].(map[string]any)
				x, ok := num(d, "remaining")
				if ok {
					n := "window:5h"
					if strings.Contains(strings.ToLower(fmt.Sprint(m["type"])), "week") {
						n = "window:weekly"
					}
					out = append(out, pct(n, x))
				}
			}
		}
		if m, ok := v["usage"].(map[string]any); ok {
			if x, ok := num(m, "remaining"); ok {
				out = append(out, pct("window:weekly", x))
			}
		}
	case "zhipu":
		m, _ := v["data"].(map[string]any)
		ls, _ := m["limits"].([]any)
		for _, r := range ls {
			x, _ := r.(map[string]any)
			if strings.EqualFold(fmt.Sprint(x["type"]), "TOKENS_LIMIT") {
				used, ok := num(x, "percentage")
				if !ok {
					continue
				}
				name := "window:quota"
				if fmt.Sprint(x["unit"]) == "3" {
					name = "window:5h"
				}
				if fmt.Sprint(x["unit"]) == "6" {
					name = "window:weekly"
				}
				out = append(out, pct(name, 100-used))
			}
		}
	case "minimax":
		ls, _ := v["model_remains"].([]any)
		for _, r := range ls {
			m, _ := r.(map[string]any)
			if fmt.Sprint(m["model_name"]) != "general" {
				continue
			}
			if x, ok := num(m, "current_interval_remaining_percent"); ok {
				out = append(out, pct("window:5h", x))
			}
			if fmt.Sprint(m["current_weekly_status"]) == "1" {
				if x, ok := num(m, "current_weekly_remaining_percent"); ok {
					out = append(out, pct("window:weekly", x))
				}
			}
		}
	case "zenmux":
		d, _ := v["data"].(map[string]any)
		for _, item := range []struct{ k, n string }{{"quota_5_hour", "window:5h"}, {"quota_7_day", "window:weekly"}} {
			m, _ := d[item.k].(map[string]any)
			if x, ok := num(m, "usage_percentage"); ok {
				out = append(out, pct(item.n, (1-x)*100))
			}
		}
	}
	if len(out) > usageMaxEntries {
		return nil, &usageFailure{message: "usage query returned too many entries"}
	}
	return out, nil
}

func (s *ProviderUsageService) executeScript(ctx context.Context, c usageConfig, script string, sameOrigin bool) ([]UsageData, *usageFailure) {
	if len(script) == 0 || len(script) > usageMaxScriptBytes {
		return nil, &usageFailure{message: "usage script size is invalid"}
	}
	vars := map[string]string{"apiKey": c.APIKey, "baseUrl": strings.TrimRight(c.BaseURL, "/"), "accessToken": c.AccessToken, "userId": c.UserID}
	rendered := script
	for k, v := range vars {
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, &usageFailure{message: "unable to prepare usage script variables"}
		}
		escaped := strings.Trim(string(encoded), `"`)
		rendered = strings.ReplaceAll(rendered, "{{"+k+"}}", escaped)
	}
	reqObj, extractor, err := evalUsageScript(rendered, nil)
	if err != nil {
		return nil, &usageFailure{message: err.Error()}
	}
	reqMap, ok := reqObj.(map[string]any)
	if !ok {
		return nil, &usageFailure{message: "usage script is missing request"}
	}
	rawURL, _ := reqMap["url"].(string)
	u, e := usageURL(rawURL)
	if e != nil {
		return nil, &usageFailure{message: e.Error()}
	}
	if sameOrigin {
		base, _ := usageURL(c.BaseURL)
		if base == nil || !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
			return nil, &usageFailure{message: "standard usage templates must use configured Base URL origin"}
		}
	}
	method, _ := reqMap["method"].(string)
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)
	if method != "GET" && method != "POST" {
		return nil, &usageFailure{message: "usage script request method must be GET or POST"}
	}
	headers := map[string]string{}
	if h, ok := reqMap["headers"].(map[string]any); ok {
		if len(h) > usageMaxHeaders {
			return nil, &usageFailure{message: "usage script request has too many headers"}
		}
		for k, v := range h {
			value, ok := v.(string)
			if !ok || !regexp.MustCompile(`^[!#$%&'*+.^_`+"`"+`|~0-9A-Za-z-]+$`).MatchString(k) || strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") || strings.EqualFold(k, "connection") || strings.EqualFold(k, "proxy-authorization") {
				return nil, &usageFailure{message: "usage script request contains an invalid header"}
			}
			headers[k] = value
		}
	}
	body, _ := reqMap["body"].(string)
	if len(body) > usageMaxBodyBytes {
		return nil, &usageFailure{message: "usage script request body is too large"}
	}
	response, status, f := s.httpJSON(ctx, u.String(), method, headers, body)
	if f != nil {
		return nil, f
	}
	if status < 200 || status >= 300 {
		return nil, usageHTTPFailure(status)
	}
	result, err := extractor(response)
	if err != nil {
		return nil, &usageFailure{message: err.Error()}
	}
	_ = extractorsNeverHost(extractor)
	return result, nil
}

func extractorsNeverHost(any) bool { return true }
func evalUsageScript(script string, response map[string]any) (any, func(map[string]any) ([]UsageData, error), error) {
	_ = response
	vm := goja.New()
	vm.SetMaxCallStackSize(512)
	timer := time.AfterFunc(usageScriptTimeout, func() { vm.Interrupt("usage script timeout") })
	v, err := vm.RunString(script)
	timer.Stop()
	vm.ClearInterrupt()
	if err != nil {
		return nil, nil, fmt.Errorf("usage script could not be evaluated")
	}
	obj := v.ToObject(vm)
	req := obj.Get("request")
	reqJSON, err := json.Marshal(req.Export())
	if err != nil {
		return nil, nil, fmt.Errorf("usage script request could not be serialized")
	}
	var request map[string]any
	if err = json.Unmarshal(reqJSON, &request); err != nil {
		return nil, nil, fmt.Errorf("usage script request has invalid shape")
	}
	fn, ok := goja.AssertFunction(obj.Get("extractor"))
	if !ok {
		return request, nil, fmt.Errorf("usage script is missing extractor")
	}
	return request, func(resp map[string]any) ([]UsageData, error) {
		timer := time.AfterFunc(usageScriptTimeout, func() { vm.Interrupt("usage script timeout") })
		rv := vm.ToValue(resp)
		got, err := fn(goja.Undefined(), rv)
		timer.Stop()
		vm.ClearInterrupt()
		if err != nil {
			return nil, fmt.Errorf("usage script extractor failed")
		}
		b, e := json.Marshal(got.Export())
		if e != nil {
			return nil, fmt.Errorf("usage script result could not be serialized")
		}
		var val any
		if e = json.Unmarshal(b, &val); e != nil {
			return nil, fmt.Errorf("usage script result is not valid JSON")
		}
		return parseUsageScriptResult(val)
	}, nil
}
func parseUsageScriptResult(v any) ([]UsageData, error) {
	items := []any{v}
	if a, ok := v.([]any); ok {
		if len(a) == 0 {
			return nil, errors.New("usage script returned an empty result")
		}
		items = a
	}
	if len(items) > usageMaxEntries {
		return nil, errors.New("usage script returned too many entries")
	}
	out := make([]UsageData, 0, len(items))
	for _, raw := range items {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("usage script result entries must be objects")
		}
		d := UsageData{}
		d.PlanName, _ = m["planName"].(string)
		if d.PlanName == "" {
			d.PlanName, _ = m["label"].(string)
		}
		d.Extra, _ = m["extra"].(string)
		d.InvalidMessage, _ = m["invalidMessage"].(string)
		d.Unit, _ = m["unit"].(string)
		if x, ok := m["isValid"].(bool); ok {
			d.IsValid = &x
		}
		for k, p := range map[string]**float64{"total": &d.Total, "used": &d.Used, "remaining": &d.Remaining} {
			if x, ok := m[k].(float64); ok && !math.IsNaN(x) && !math.IsInf(x, 0) {
				*p = &x
			}
		}
		if d.PlanName == "" && d.Extra == "" && d.InvalidMessage == "" && d.Unit == "" && d.IsValid == nil && d.Total == nil && d.Used == nil && d.Remaining == nil {
			return nil, errors.New("usage script result entry is empty")
		}
		out = append(out, d)
	}
	return out, nil
}

func (s *ProviderUsageService) httpJSON(ctx context.Context, raw, method string, headers map[string]string, body string) (map[string]any, int, *usageFailure) {
	timeout := usageDefaultTimeout
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, e := http.NewRequestWithContext(reqCtx, method, raw, bytes.NewBufferString(body))
	if e != nil {
		return nil, 0, &usageFailure{message: "usage query request is invalid"}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return nil, 0, &usageFailure{message: "usage query request failed", transient: true}
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, usageMaxResponseBytes+1)
	b, e := io.ReadAll(limited)
	if e != nil || len(b) > usageMaxResponseBytes {
		return nil, resp.StatusCode, &usageFailure{message: "usage query response is too large", transient: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, nil
	}
	var out map[string]any
	if len(bytes.TrimSpace(b)) > 0 {
		if e = json.Unmarshal(b, &out); e != nil {
			return nil, resp.StatusCode, &usageFailure{message: "usage query response is not valid JSON"}
		}
	}
	return out, resp.StatusCode, nil
}
