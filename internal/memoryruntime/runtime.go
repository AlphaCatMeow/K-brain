// Package memoryruntime owns model-backed memory injection and hidden turn extraction.
package memoryruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type ModelResolver func(context.Context, string) (ai.Client, string, error)

type Config struct {
	Store           *memory.Store
	ResolveModel    ModelResolver
	ExtractionModel string
	OrganizerModel  string
	Throttle        time.Duration
	Now             func() time.Time
}

type Runtime struct {
	store      *memory.Store
	resolve    ModelResolver
	extraction string
	organizer  string
	throttle   time.Duration
	now        func() time.Time
	mu         sync.Mutex
	claims     map[string]*claim
	history    map[string]OrganizerRun
	closed     bool
}

type claim struct {
	cancel  context.CancelFunc
	timer   *time.Timer
	last    time.Time
	pending *agent.MemoryTurn
	parent  context.Context
}

func New(cfg Config) (*Runtime, error) {
	if cfg.Store == nil {
		return nil, errors.New("memory runtime store is required")
	}
	if cfg.ResolveModel == nil {
		return nil, errors.New("memory runtime model resolver is required")
	}
	if cfg.Throttle <= 0 {
		cfg.Throttle = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Runtime{store: cfg.Store, resolve: cfg.ResolveModel, extraction: cfg.ExtractionModel, organizer: cfg.OrganizerModel, throttle: cfg.Throttle, now: cfg.Now, claims: map[string]*claim{}, history: map[string]OrganizerRun{}}, nil
}

func (r *Runtime) Inject(ctx context.Context, workdir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	overview, err := r.store.Overview(workdir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Memory Index (reference only; verify before relying on it):\n")
	write := func(label string, entries []memory.Meta) {
		if len(entries) == 0 {
			return
		}
		b.WriteString(label + ":\n")
		for _, e := range entries {
			line := fmt.Sprintf("- %s [%s/%s] %s", e.Slug, e.Scope, e.MemoryType, e.Description)
			if e.Confidence != "" && e.Confidence != "unknown" {
				line += " (" + e.Confidence + ")"
			}
			if len([]rune(line)) > 500 {
				line = string([]rune(line)[:500])
			}
			b.WriteString(line + "\n")
		}
	}
	write("User", overview.User)
	write("Project", overview.Project)
	write("Global", overview.Global)
	write("Recent", overview.RecentDays)
	out := []rune(b.String())
	if len(out) > 16*1024 {
		out = out[:16*1024]
	}
	return string(out), nil
}

func (r *Runtime) Tools(workdir func() string) []tools.Tool {
	return []tools.Tool{memoryManagerTool(r.store, workdir)}
}

func (r *Runtime) Completed(parent context.Context, turn agent.MemoryTurn) {
	if strings.TrimSpace(turn.User.TextContent()) == "" || isAcknowledgement(turn.User.TextContent()) || r.extraction == "" {
		return
	}
	key := turn.SessionID
	if key == "" {
		key = "global"
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	c := r.claims[key]
	if c == nil {
		c = &claim{}
		r.claims[key] = c
	}
	if c.pending != nil && c.pending.User.ID == turn.User.ID {
		r.mu.Unlock()
		return
	}
	if c.pending != nil && c.pending.User.TextContent() == turn.User.TextContent() {
		r.mu.Unlock()
		return
	}
	c.pending = &turn
	c.parent = parent
	now := r.now()
	if c.cancel != nil {
		r.mu.Unlock()
		return
	}
	if !c.last.IsZero() && now.Sub(c.last) < r.throttle {
		if c.timer == nil {
			wait := r.throttle - now.Sub(c.last)
			c.timer = time.AfterFunc(wait, func() { r.startPending(key) })
		}
		r.mu.Unlock()
		return
	}
	pending := *c.pending
	c.pending = nil
	c.last = now
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	r.mu.Unlock()
	go r.extract(ctx, key, pending)
}

func (r *Runtime) startPending(key string) {
	r.mu.Lock()
	c := r.claims[key]
	if c == nil || c.cancel != nil || c.pending == nil || r.closed {
		r.mu.Unlock()
		return
	}
	c.timer = nil
	pending := *c.pending
	c.pending = nil
	c.last = r.now()
	ctx, cancel := context.WithCancel(c.parent)
	c.cancel = cancel
	r.mu.Unlock()
	go r.extract(ctx, key, pending)
}

func (r *Runtime) extract(ctx context.Context, key string, turn agent.MemoryTurn) {
	defer func() { r.finishClaim(key) }()
	messages := extractionMessages(turn)
	plan, err := r.submit(ctx, r.extraction, messages, false)
	if err != nil || plan == nil {
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("memory extraction %s: %v; retrying", key, err)
		}
		plan, err = r.submit(ctx, r.extraction, messages, true)
	}
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("memory extraction %s: %v", key, err)
		}
		return
	}
	if plan == nil || len(plan.Decisions) == 0 {
		return
	}
	batch, ok := extractionBatch(plan.Decisions, turn)
	if !ok || ctx.Err() != nil {
		return
	}
	result, err := r.store.ApplyBatch(batch)
	if err != nil {
		log.Printf("memory extraction %s: apply batch: %v", key, err)
		return
	}
	for _, warning := range result.WarningDetails {
		log.Printf("memory extraction %s: decision %d (%s %s) not applied: %s", key, warning.DecisionIndex, warning.Op, warning.Slug, warning.Code)
	}
}

func (r *Runtime) finishClaim(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.claims[key]; c != nil {
		c.cancel = nil
		if c.pending != nil && c.timer == nil && !r.closed {
			wait := r.throttle - r.now().Sub(c.last)
			if wait < 0 {
				wait = 0
			}
			c.timer = time.AfterFunc(wait, func() { r.startPending(key) })
		}
	}
}

func extractionMessages(turn agent.MemoryTurn) []ai.Message {
	users := make([]string, 0, 4)
	for i := len(turn.History) - 1; i >= 0 && len(users) < 4; i-- {
		if turn.History[i].Role == "user" {
			users = append(users, truncateRunes(turn.History[i].TextContent(), 2000))
		}
	}
	for i, j := 0, len(users)-1; i < j; i, j = i+1, j-1 {
		users[i], users[j] = users[j], users[i]
	}
	content := "Extract only durable facts from these user turns.\n\n" + strings.Join(users, "\n---\n")
	return []ai.Message{{Role: "system", Content: extractionPrompt}, {Role: "user", Content: content}}
}

const extractionPrompt = `You are K-brain's silent memory extractor. Do not answer the user. Submit at most one SubmitMemoryPlan tool call, following its schema exactly. Each decision names an action: write (new memory: slug, scope, type, description, body), update (existing slug with changed fields), accept, delete, or append_daily (body is one bullet for today's journal). Quote the user's own words in source_quote. If there is no durable fact, do not submit a plan.`

func (r *Runtime) submit(ctx context.Context, model string, messages []ai.Message, retry bool) (*planEnvelope, error) {
	client, resolved, err := r.resolve(ctx, model)
	if err != nil {
		return nil, err
	}
	if resolved != "" {
		model = resolved
	}
	prompt := messages
	if retry {
		prompt = append(append([]ai.Message(nil), messages...), ai.Message{Role: "user", Content: "Submit the plan now using the SubmitMemoryPlan tool, or submit nothing if there is no durable memory."})
	}
	var raw string
	var call bool
	_, _, err = client.Stream(ctx, ai.Request{Model: model, Messages: prompt, Tools: []ai.Tool{submitTool()}, MaxTokens: 2048}, func(string) {}, func(string) {}, func(_ string, name string, args string) {
		if name == "SubmitMemoryPlan" {
			raw = args
			call = true
		}
	})
	if err != nil {
		return nil, err
	}
	if !call || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return parsePlan(raw)
}

type planEnvelope struct {
	Decisions []memory.Decision
}

type rawPlan struct {
	Decisions []json.RawMessage `json:"decisions"`
	Plan      []json.RawMessage `json:"plan"`
}

// planItem is one decision as SubmitMemoryPlan advertises it. Op and MemoryType
// keep accepting the store's own field names.
type planItem struct {
	Action      string           `json:"action"`
	Op          string           `json:"op"`
	Slug        string           `json:"slug"`
	Scope       string           `json:"scope"`
	Type        string           `json:"type"`
	MemoryType  string           `json:"memoryType"`
	Description *string          `json:"description"`
	Body        *string          `json:"body"`
	Mode        string           `json:"mode"`
	Confidence  string           `json:"confidence"`
	SourceQuote string           `json:"source_quote"`
	Reasoning   string           `json:"reasoning"`
	Evidence    *memory.Evidence `json:"evidence"`
}

// parsePlan keeps every well-formed decision; a malformed one is logged and
// dropped instead of discarding the whole plan.
func parsePlan(raw string) (*planEnvelope, error) {
	var envelope rawPlan
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, fmt.Errorf("decode memory plan: %w", err)
	}
	items := envelope.Decisions
	if len(items) == 0 {
		items = envelope.Plan
	}
	p := &planEnvelope{}
	for i, item := range items {
		d, err := decodeDecision(item)
		if err == nil {
			err = validateDecision(d)
		}
		if err != nil {
			log.Printf("memory extraction: dropped plan decision %d: %v", i, err)
			continue
		}
		p.Decisions = append(p.Decisions, d)
	}
	if len(items) > 0 && len(p.Decisions) == 0 {
		return nil, errors.New("memory plan had no valid decisions")
	}
	return p, nil
}

func decodeDecision(raw json.RawMessage) (memory.Decision, error) {
	var item planItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return memory.Decision{}, err
	}
	op := item.Op
	if op == "" {
		op = item.Action
	}
	if op == "write" {
		op = "upsert"
	}
	memoryType := item.MemoryType
	if memoryType == "" {
		memoryType = item.Type
	}
	evidence := item.Evidence
	if evidence == nil && (item.Confidence != "" || item.SourceQuote != "" || item.Reasoning != "") {
		evidence = &memory.Evidence{Confidence: item.Confidence, SourceQuote: item.SourceQuote, Reasoning: item.Reasoning}
	}
	return memory.Decision{Op: op, Slug: item.Slug, Scope: item.Scope, MemoryType: memoryType, Description: item.Description, Body: item.Body, Mode: item.Mode, Evidence: evidence}, nil
}

const submitPlanSchema = `{"type":"object","properties":{"decisions":{"type":"array","items":{"type":"object","properties":{` +
	`"action":{"type":"string","enum":["write","update","accept","delete","append_daily"]},` +
	`"slug":{"type":"string","description":"Stable kebab-case id, e.g. user-reply-language; required except for append_daily"},` +
	`"scope":{"type":"string","enum":["global","project"],"description":"project only for facts specific to the current workspace"},` +
	`"type":{"type":"string","enum":["user","feedback","project","reference"]},` +
	`"description":{"type":"string","description":"One-line summary, at most 120 characters"},` +
	`"body":{"type":"string","description":"The memory content; for append_daily, one journal bullet"},` +
	`"mode":{"type":"string","enum":["merge","replace"],"description":"update only"},` +
	`"confidence":{"type":"string","enum":["high","medium","low"]},` +
	`"source_quote":{"type":"string","description":"The user's own words supporting this decision"},` +
	`"reasoning":{"type":"string"}` +
	`},"required":["action"]}}},"required":["decisions"]}`

func submitTool() ai.Tool {
	return ai.NewTool("SubmitMemoryPlan", "Submit validated memory mutations for one atomic batch.", submitPlanSchema)
}

func extractionBatch(decisions []memory.Decision, turn agent.MemoryTurn) (memory.BatchArgs, bool) {
	batch := memory.BatchArgs{Workdir: turn.Workdir, ConversationID: turn.SessionID, Trigger: "extraction", Model: turn.Model}
	for _, d := range decisions {
		if d.Op == "append_daily" {
			if d.Body == nil || strings.TrimSpace(*d.Body) == "" {
				continue
			}
			batch.DailyAppend = &struct {
				Bullet string `json:"bullet"`
			}{Bullet: strings.TrimSpace(*d.Body)}
			continue
		}
		if d.Scope == "project" && strings.TrimSpace(turn.Workdir) == "" {
			continue
		}
		batch.Decisions = append(batch.Decisions, d)
	}
	return batch, len(batch.Decisions) > 0 || batch.DailyAppend != nil
}

func validateDecision(d memory.Decision) error {
	switch d.Op {
	case "upsert", "update", "accept", "delete":
	case "append_daily":
		if d.Body == nil || strings.TrimSpace(*d.Body) == "" {
			return errors.New("append_daily requires body")
		}
	default:
		return fmt.Errorf("unsupported memory plan action %q", d.Op)
	}
	if d.Op != "append_daily" && strings.TrimSpace(d.Slug) == "" {
		return errors.New("memory plan slug required")
	}
	if d.Op == "upsert" && (d.Description == nil || d.Body == nil || strings.TrimSpace(*d.Description) == "" || strings.TrimSpace(*d.Body) == "") {
		return errors.New("upsert requires description and body")
	}
	return nil
}

func isAcknowledgement(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "ok", "okay", "thanks", "thank you", "thx", "hi", "hello", "hey", "谢谢", "好的", "收到":
		return true
	}
	return false
}
func truncateRunes(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	x := []rune(s)
	if len(x) > n {
		return string(x[:n])
	}
	return s
}

func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for _, c := range r.claims {
		if c.cancel != nil {
			c.cancel()
		}
	}
	return nil
}

var _ agent.MemoryRuntime = (*Runtime)(nil)
