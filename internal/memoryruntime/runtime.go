// Package memoryruntime owns model-backed memory injection and hidden turn extraction.
package memoryruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

type ModelResolver func(context.Context, string) (ai.Client, string, error)

// logf writes extraction diagnostics to k-brain.log: hosts such as LiveAgent
// drain and drop the backend's stderr.
var logf = func(format string, args ...any) { config.LogEvent("memory", fmt.Sprintf(format, args...)) }

// extractionRetryDelays spaces out retries of a failed extraction request;
// busy providers often recover within a minute.
var extractionRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}

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
	// unjudged holds the user messages of turns coalesced into the pending
	// one; they are judged together so none is skipped.
	unjudged []ai.Message
	// inflight holds the messages of the running extraction, kept by Close
	// so a cancelled extraction never writes after it.
	inflight []ai.Message
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
	own := strings.TrimSpace(turn.User.TextContent())
	judgeOwn := own != "" && !isAcknowledgement(own)
	if r.extraction == "" {
		return
	}
	// The backend cancels the run's context as soon as the run returns, right
	// after this call; extraction outlives the run and stops only on Close.
	parent = context.WithoutCancel(parent)
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
	if c.pending != nil && (c.pending.User.ID == turn.User.ID || c.pending.User.TextContent() == turn.User.TextContent()) {
		judgeOwn = false
	}
	queued := make(map[string]bool, len(c.unjudged))
	for _, m := range c.unjudged {
		queued[m.ID] = true
	}
	var add []ai.Message
	for _, m := range r.heldUserMessages(key, turn) {
		if !queued[m.ID] {
			queued[m.ID] = true
			add = append(add, m)
		}
	}
	if judgeOwn && (turn.User.ID == "" || !queued[turn.User.ID]) {
		add = append(add, turn.User)
	}
	if len(add) == 0 {
		r.mu.Unlock()
		return
	}
	c.pending = &turn
	c.parent = parent
	c.unjudged = append(c.unjudged, add...)
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
	pending, judge := *c.pending, c.unjudged
	c.pending, c.unjudged = nil, nil
	c.last = now
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.inflight = judge
	r.mu.Unlock()
	go r.extract(ctx, key, pending, judge)
}

// Abandoned keeps the user message of a stopped or failed turn for the
// session's next completed turn; it stays in History, which later turns treat
// as context only.
func (r *Runtime) Abandoned(sessionID string, user ai.Message) {
	if sessionID == "" {
		sessionID = "global"
	}
	r.hold(sessionID, []ai.Message{user})
}

// holdUnlessClosed keeps messages of a failed extraction; after Close they
// were already kept and the runtime must not write again.
func (r *Runtime) holdUnlessClosed(key string, msgs []ai.Message) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if !closed {
		r.hold(key, msgs)
	}
}

// maxJudgedRunes is how much of a user message the extraction prompt shows,
// and so all that a held message needs to keep.
const maxJudgedRunes = 2000

// hold persists user messages that still need judging.
func (r *Runtime) hold(key string, msgs []ai.Message) {
	var held []memory.UnjudgedMessage
	for _, m := range msgs {
		if text := strings.TrimSpace(m.TextContent()); m.ID != "" && text != "" {
			held = append(held, memory.UnjudgedMessage{ID: m.ID, Text: truncateRunes(text, maxJudgedRunes)})
		}
	}
	if err := r.store.HoldUnjudged(key, held); err != nil {
		logf("memory extraction %s: hold unjudged messages: %s", key, describeExtractionError(err))
	}
}

// heldUserMessages takes the session's held messages that are still in
// History (an edit or rewind removes them) and are not the turn's own (a
// resumed turn judges its message itself).
func (r *Runtime) heldUserMessages(key string, turn agent.MemoryTurn) []ai.Message {
	held, err := r.store.TakeUnjudged(key)
	if err != nil {
		logf("memory extraction %s: read unjudged messages: %s", key, describeExtractionError(err))
		return nil
	}
	present := make(map[string]bool, len(turn.History))
	for _, m := range turn.History {
		if m.Role == "user" && m.ID != "" {
			present[m.ID] = true
		}
	}
	var out []ai.Message
	for _, h := range held {
		if h.ID == turn.User.ID || !present[h.ID] || isAcknowledgement(h.Text) {
			continue
		}
		out = append(out, ai.Message{ID: h.ID, Role: "user", Content: h.Text})
	}
	return out
}

func (r *Runtime) startPending(key string) {
	r.mu.Lock()
	c := r.claims[key]
	if c == nil || c.cancel != nil || c.pending == nil || r.closed {
		r.mu.Unlock()
		return
	}
	c.timer = nil
	pending, judge := *c.pending, c.unjudged
	c.pending, c.unjudged = nil, nil
	c.last = r.now()
	ctx, cancel := context.WithCancel(c.parent)
	c.cancel = cancel
	c.inflight = judge
	r.mu.Unlock()
	go r.extract(ctx, key, pending, judge)
}

func (r *Runtime) extract(ctx context.Context, key string, turn agent.MemoryTurn, judge []ai.Message) {
	defer func() { r.finishClaim(key) }()
	existing := r.existingMemory(turn.Workdir)
	messages := extractionMessages(turn, existing, judge)
	plan, nudged, err := r.submitRetrying(ctx, key, messages, false)
	if err == nil && plan == nil && !nudged && ctx.Err() == nil {
		plan, _, err = r.submitRetrying(ctx, key, messages, true)
	}
	if err != nil {
		if ctx.Err() == nil {
			logf("memory extraction %s: %s", key, describeExtractionError(err))
		}
		// Not judged: keep the messages for the next completed turn.
		r.holdUnlessClosed(key, judge)
		return
	}
	if plan == nil || len(plan.Decisions) == 0 {
		return
	}
	batch, ok := extractionBatch(plan.Decisions, turn, existing)
	if !ok || ctx.Err() != nil {
		return
	}
	result, err := r.store.ApplyBatch(batch)
	if err != nil {
		logf("memory extraction %s: apply batch: %s", key, describeExtractionError(err))
		r.holdUnlessClosed(key, judge)
		return
	}
	for _, warning := range result.WarningDetails {
		logf("memory extraction %s: decision %d (%s %s) not applied: %s", key, warning.DecisionIndex, warning.Op, warning.Slug, warning.Code)
	}
}

// submitRetrying retries failed model requests on extractionRetryDelays. A plan
// the model got wrong is retried once by the caller with a nudge instead.
func (r *Runtime) submitRetrying(ctx context.Context, key string, messages []ai.Message, nudge bool) (*planEnvelope, bool, error) {
	plan, err := r.submit(ctx, r.extraction, messages, nudge)
	var bad planError
	if err != nil && errors.As(err, &bad) && !nudge {
		logf("memory extraction %s: %s; retrying", key, describeExtractionError(err))
		plan, _, err = r.submitRetrying(ctx, key, messages, true)
		return plan, true, err
	}
	for _, delay := range extractionRetryDelays {
		if err == nil || ctx.Err() != nil || permanentModelError(err) {
			break
		}
		if errors.As(err, &bad) {
			if nudge {
				break
			}
			logf("memory extraction %s: %s; retrying", key, describeExtractionError(err))
			plan, _, err = r.submitRetrying(ctx, key, messages, true)
			return plan, true, err
		}
		logf("memory extraction %s: %s; retrying in %s", key, describeExtractionError(err), delay)
		select {
		case <-ctx.Done():
			return nil, nudge, ctx.Err()
		case <-time.After(delay):
		}
		plan, err = r.submit(ctx, r.extraction, messages, nudge)
	}
	if err != nil && errors.As(err, &bad) && !nudge {
		logf("memory extraction %s: %s; retrying", key, describeExtractionError(err))
		plan, _, err = r.submitRetrying(ctx, key, messages, true)
		return plan, true, err
	}
	return plan, nudge, err
}

// permanentModelError reports client errors a retry cannot fix (bad key,
// exhausted quota); 429 rate limits are still retried.
func permanentModelError(err error) bool {
	var httpErr *ai.HTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 && httpErr.StatusCode != 429
}

func (r *Runtime) finishClaim(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.claims[key]; c != nil {
		c.cancel = nil
		c.inflight = nil
		if c.pending != nil && c.timer == nil && !r.closed {
			wait := r.throttle - r.now().Sub(c.last)
			if wait < 0 {
				wait = 0
			}
			c.timer = time.AfterFunc(wait, func() { r.startPending(key) })
		}
	}
}

// existingMemory lists the entries the extractor must reuse instead of
// re-creating; a failed read only loses deduplication, not the turn.
func (r *Runtime) existingMemory(workdir string) []memory.Meta {
	list, err := r.store.List(memory.ListArgs{Workdir: workdir, Limit: 10000})
	if err != nil {
		logf("memory extraction: list existing memory: %s", describeExtractionError(err))
		return nil
	}
	active := list.Entries[:0]
	for _, e := range list.Entries {
		if !e.Archived {
			active = append(active, e)
		}
	}
	return active
}

// maxPromptEntries bounds the existing-memory block; deduplication and the
// new-slug gate still see every entry.
const maxPromptEntries = 200

func extractionMessages(turn agent.MemoryTurn, existing []memory.Meta, judge []ai.Message) []ai.Message {
	if len(judge) == 0 {
		judge = []ai.Message{turn.User}
	}
	judged := make(map[string]bool, len(judge))
	latest := make([]string, 0, len(judge))
	for _, m := range judge {
		text := truncateRunes(m.TextContent(), maxJudgedRunes)
		judged[text] = true
		latest = append(latest, text)
	}
	earlier := make([]string, 0, 3)
	for i := len(turn.History) - 1; i >= 0 && len(earlier) < 3; i-- {
		if turn.History[i].Role != "user" {
			continue
		}
		text := truncateRunes(turn.History[i].TextContent(), 2000)
		if judged[text] {
			continue
		}
		earlier = append(earlier, text)
	}
	for i, j := 0, len(earlier)-1; i < j; i, j = i+1, j-1 {
		earlier[i], earlier[j] = earlier[j], earlier[i]
	}
	var b strings.Builder
	b.WriteString("<existing-memory>\n")
	if len(existing) == 0 {
		b.WriteString("- (none)\n")
	}
	for i, e := range existing {
		if i == maxPromptEntries {
			fmt.Fprintf(&b, "- (%d more entries not shown)\n", len(existing)-i)
			break
		}
		fmt.Fprintf(&b, "- %s (type=%s; scope=%s) %s\n", e.Slug, e.MemoryType, e.Scope, truncateRunes(e.Description, 160))
	}
	b.WriteString("</existing-memory>\n\n<earlier-user-messages context-only=\"true\">\n")
	if len(earlier) == 0 {
		b.WriteString("(none)\n")
	}
	for _, text := range earlier {
		b.WriteString(text + "\n---\n")
	}
	b.WriteString("</earlier-user-messages>\n\n<latest-user-message>\n" + strings.Join(latest, "\n---\n") + "\n</latest-user-message>")
	return []ai.Message{{Role: "system", Content: extractionPrompt}, {Role: "user", Content: b.String()}}
}

const extractionPrompt = `You are K-brain's silent memory extractor. Do not answer the user. Submit at most one SubmitMemoryPlan tool call, following its schema exactly. Each decision names an action: write (new memory: slug, scope, type, description, body), update (existing slug with changed fields), delete, or append_daily (body is one bullet for today's journal). Quote the user's own words in source_quote.

Judge only <latest-user-message>, which may hold several messages separated by ---. <earlier-user-messages> is context for understanding it; anything in them was already handled on earlier turns, so never extract from them again.

Durable memory is what should still apply in a later, unrelated conversation: the user's lasting preferences and instructions for how to work or answer (language, format, tools, style; type user or feedback), facts about the user, project conventions (type project, scope project), and pointers to external resources the user will keep using (type reference). Anything the user explicitly asks you to remember is durable. Signal words include 记住, 以后, 总是, 每次, 不要, 别再, remember, from now on, always, never, prefer.

Skip: instructions for the current task only (which repo, URL, file or value to use this time), greetings, questions, one-off answers, facts derivable from the workspace, secrets and credentials, raw code or logs, and requests about memory itself. When in doubt, skip.

Before writing, check <existing-memory>. If an entry already covers the fact, update that slug only when the latest message changes it, otherwise skip; never create a second entry for the same fact. Create a new slug only at high confidence, or medium with an unambiguous source_quote.

Write description and body in the language the user writes in. If nothing qualifies, do not submit a plan.`

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
	Action      string          `json:"action"`
	Op          string          `json:"op"`
	Slug        string          `json:"slug"`
	Scope       string          `json:"scope"`
	Type        string          `json:"type"`
	MemoryType  string          `json:"memoryType"`
	Description *string         `json:"description"`
	Body        *string         `json:"body"`
	Mode        string          `json:"mode"`
	Confidence  string          `json:"confidence"`
	SourceQuote string          `json:"source_quote"`
	Reasoning   string          `json:"reasoning"`
	Evidence    json.RawMessage `json:"evidence"`
}

// planError marks extractor-side plan problems; their text names fields, not
// user content, so it is safe to log.
type planError struct{ error }

// describeExtractionError keeps provider bodies (credentials, echoed prompts)
// out of the log.
func describeExtractionError(err error) string {
	var httpErr *ai.HTTPError
	if errors.As(err, &httpErr) {
		return fmt.Sprintf("model request failed with HTTP %d", httpErr.StatusCode)
	}
	var plan planError
	if errors.As(err, &plan) {
		return plan.Error()
	}
	return fmt.Sprintf("model request failed (%T)", err)
}

// parsePlan keeps every well-formed decision; a malformed one is logged and
// dropped instead of discarding the whole plan.
func parsePlan(raw string) (*planEnvelope, error) {
	var envelope rawPlan
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, planError{fmt.Errorf("decode memory plan: %w", err)}
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
			logf("memory extraction: dropped plan decision %d: %v", i, err)
			continue
		}
		p.Decisions = append(p.Decisions, d)
	}
	if len(items) > 0 && len(p.Decisions) == 0 {
		return nil, planError{errors.New("memory plan had no valid decisions")}
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
	evidence := decodeEvidence(item.Evidence)
	if evidence == nil && (item.Confidence != "" || item.SourceQuote != "" || item.Reasoning != "") {
		evidence = &memory.Evidence{}
	}
	// Top-level fields fill what an evidence object or quote string left out.
	if evidence != nil {
		if evidence.Confidence == "" {
			evidence.Confidence = item.Confidence
		}
		if evidence.SourceQuote == "" {
			evidence.SourceQuote = item.SourceQuote
		}
		if evidence.Reasoning == "" {
			evidence.Reasoning = item.Reasoning
		}
	}
	return memory.Decision{Op: op, Slug: item.Slug, Scope: item.Scope, MemoryType: memoryType, Description: item.Description, Body: item.Body, Mode: item.Mode, Evidence: evidence}, nil
}

// decodeEvidence accepts the store's evidence object or a bare quote string;
// anything else is ignored rather than dropping the decision.
func decodeEvidence(raw json.RawMessage) *memory.Evidence {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var quote string
	if json.Unmarshal(raw, &quote) == nil {
		if quote = strings.TrimSpace(quote); quote != "" {
			return &memory.Evidence{SourceQuote: quote}
		}
		return nil
	}
	var evidence memory.Evidence
	if json.Unmarshal(raw, &evidence) == nil {
		return &evidence
	}
	return nil
}

const submitPlanSchema = `{"type":"object","properties":{"decisions":{"type":"array","items":{"type":"object","properties":{` +
	`"action":{"type":"string","enum":["write","update","delete","append_daily"]},` +
	`"slug":{"type":"string","description":"Stable kebab-case id, e.g. user-reply-language; required except for append_daily"},` +
	`"scope":{"type":"string","enum":["global","project"],"description":"project only for facts specific to the current workspace"},` +
	`"type":{"type":"string","enum":["user","feedback","project","reference"]},` +
	`"description":{"type":"string","description":"One-line summary, at most 120 characters"},` +
	`"body":{"type":"string","description":"The memory content; for append_daily, one journal bullet"},` +
	`"mode":{"type":"string","enum":["merge","replace"],"description":"update only"},` +
	`"confidence":{"type":"string","enum":["high","medium","low"],"description":"Required for write: a new memory needs high, or medium with source_quote"},` +
	`"source_quote":{"type":"string","description":"The user's own words supporting this decision"},` +
	`"reasoning":{"type":"string"}` +
	`},"required":["action"]}}},"required":["decisions"]}`

func submitTool() ai.Tool {
	return ai.NewTool("SubmitMemoryPlan", "Submit validated memory mutations for one atomic batch.", submitPlanSchema)
}

func extractionBatch(decisions []memory.Decision, turn agent.MemoryTurn, existing []memory.Meta) (memory.BatchArgs, bool) {
	batch := memory.BatchArgs{Workdir: turn.Workdir, ConversationID: turn.SessionID, Trigger: "extraction", Model: turn.Model}
	known := make(map[string]bool, len(existing))
	for _, e := range existing {
		known[e.Scope+"/"+e.Slug] = true
	}
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
		scope := d.Scope
		if scope == "" {
			// ApplyBatch's default scope.
			scope = "project"
			if strings.TrimSpace(turn.Workdir) == "" {
				scope = "global"
			}
		}
		if d.Op == "upsert" && !known[scope+"/"+d.Slug] && !strongNewMemory(d.Evidence) {
			logf("memory extraction %s: skipped weak new memory %s", turn.SessionID, d.Slug)
			continue
		}
		batch.Decisions = append(batch.Decisions, d)
	}
	return batch, len(batch.Decisions) > 0 || batch.DailyAppend != nil
}

// strongNewMemory gates new slugs: high confidence, or medium backed by the
// user's own words. Weaker candidates may only update existing entries.
func strongNewMemory(e *memory.Evidence) bool {
	if e == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(e.Confidence)) {
	case "high":
		return true
	case "medium":
		return len([]rune(strings.TrimSpace(e.SourceQuote))) >= 5
	}
	return false
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
	r.closed = true
	queued := map[string][]ai.Message{}
	for key, c := range r.claims {
		if c.cancel != nil {
			c.cancel()
		}
		if c.timer != nil {
			c.timer.Stop()
		}
		// Queued or still running: keep them for the session's next runtime.
		if msgs := append(append([]ai.Message(nil), c.inflight...), c.unjudged...); len(msgs) > 0 {
			queued[key] = msgs
		}
		c.unjudged, c.inflight = nil, nil
	}
	r.mu.Unlock()
	for key, msgs := range queued {
		r.hold(key, msgs)
	}
	return nil
}

var _ agent.MemoryRuntime = (*Runtime)(nil)
