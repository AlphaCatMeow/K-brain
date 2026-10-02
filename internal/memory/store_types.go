package memory

import "encoding/json"

const ScopeLimit = 500

type Meta struct {
	Slug        string  `json:"slug"`
	Scope       string  `json:"scope"`
	WorkdirHash string  `json:"workdirHash"`
	WorkdirPath string  `json:"workdirPath,omitempty"`
	MemoryType  string  `json:"memoryType"`
	Description string  `json:"description"`
	Headline    string  `json:"headline"`
	DateLocal   *string `json:"dateLocal"`
	CreatedAt   int64   `json:"createdAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	AppendCount int     `json:"appendCount"`
	Archived    bool    `json:"archived"`
	Unreviewed  bool    `json:"unreviewed"`
	Confidence  string  `json:"confidence"`
	FileSize    int64   `json:"fileSize"`
}

type ListArgs struct {
	Scope              string `json:"scope,omitempty"`
	Workdir            string `json:"workdir,omitempty"`
	IncludeAllProjects bool   `json:"includeAllProjects,omitempty"`
	MemoryType         string `json:"memoryType,omitempty"`
	IncludeDaily       bool   `json:"includeDaily,omitempty"`
	Limit              int    `json:"limit,omitempty"`
	Offset             int    `json:"offset,omitempty"`
}
type ScopeQuota struct {
	Scope                   string   `json:"scope"`
	WorkdirHash             string   `json:"workdirHash"`
	Used                    int      `json:"used"`
	Limit                   int      `json:"limit"`
	Headroom                int      `json:"headroom"`
	Archived                int      `json:"archivedCount"`
	UnreviewedCount         int      `json:"unreviewedCount"`
	OldestUnreviewedAgeDays *float64 `json:"oldestUnreviewedAgeDays"`
}
type Quota struct {
	Used        int          `json:"used"`
	Limit       int          `json:"limit"`
	ScopeQuotas []ScopeQuota `json:"scopeQuotas"`
}
type QuotaSummaryResponse struct {
	Scopes []ScopeQuota `json:"scopes"`
}
type ListResponse struct {
	Entries   []Meta `json:"entries"`
	Truncated bool   `json:"truncated"`
	Quota     Quota  `json:"quota"`
}
type ReadArgs struct {
	Slug        string `json:"slug"`
	Scope       string `json:"scope,omitempty"`
	Workdir     string `json:"workdir,omitempty"`
	WorkdirHash string `json:"workdirHash,omitempty"`
	Offset      int    `json:"offset,omitempty"`
	Length      int    `json:"length,omitempty"`
}
type ReadWindow struct {
	Offset    int  `json:"offset"`
	Length    int  `json:"length"`
	Truncated bool `json:"truncated"`
}
type ReadMeta struct {
	Unreviewed bool   `json:"unreviewed"`
	Confidence string `json:"confidence"`
	Source     any    `json:"source"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	Archived   bool   `json:"archived"`
}
type ReadResponse struct {
	Slug        string     `json:"slug"`
	Scope       string     `json:"scope"`
	MemoryType  string     `json:"memoryType"`
	Description string     `json:"description"`
	Headline    string     `json:"headline"`
	Body        string     `json:"body"`
	TotalLines  int        `json:"totalLines"`
	Window      ReadWindow `json:"window"`
	Meta        ReadMeta   `json:"meta"`
}
type Evidence struct {
	Confidence     string   `json:"confidence,omitempty"`
	SourceQuote    string   `json:"sourceQuote,omitempty"`
	Reasoning      string   `json:"reasoning,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
	ConflictsWith  []string `json:"conflictsWith,omitempty"`
	Supersedes     string   `json:"supersedes,omitempty"`
	OverrideReject string   `json:"overrideReject,omitempty"`
}
type WriteArgs struct {
	Slug           string    `json:"slug"`
	Scope          string    `json:"scope"`
	Workdir        string    `json:"workdir,omitempty"`
	MemoryType     string    `json:"memoryType"`
	Description    string    `json:"description"`
	Body           string    `json:"body"`
	Actor          string    `json:"actor,omitempty"`
	ConversationID string    `json:"conversationId,omitempty"`
	Model          string    `json:"model,omitempty"`
	Evidence       *Evidence `json:"evidence,omitempty"`
	Unreviewed     *bool     `json:"unreviewed,omitempty"`
}
type UpdateArgs struct {
	ReadArgs
	MemoryType     *string   `json:"memoryType,omitempty"`
	Description    *string   `json:"description,omitempty"`
	Body           *string   `json:"body,omitempty"`
	Mode           string    `json:"mode,omitempty"`
	Actor          string    `json:"actor,omitempty"`
	ConversationID string    `json:"conversationId,omitempty"`
	Model          string    `json:"model,omitempty"`
	Evidence       *Evidence `json:"evidence,omitempty"`
}
type DeleteArgs struct {
	ReadArgs
	Actor          string `json:"actor,omitempty"`
	Reason         string `json:"reason,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	Model          string `json:"model,omitempty"`
}
type MutationResponse struct {
	Slug              string  `json:"slug"`
	Scope             string  `json:"scope"`
	Created           bool    `json:"created"`
	Updated           bool    `json:"updated"`
	Deleted           bool    `json:"deleted"`
	IndexUpdated      bool    `json:"indexUpdated"`
	Warning           *string `json:"warning"`
	AppliedConfidence string  `json:"appliedConfidence,omitempty"`
	AutoDowngraded    *bool   `json:"autoDowngraded,omitempty"`
}
type SearchArgs struct {
	Query            string `json:"query"`
	Scope            string `json:"scope,omitempty"`
	Workdir          string `json:"workdir,omitempty"`
	MemoryType       string `json:"memoryType,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	IncludeHistory   bool   `json:"includeHistory,omitempty"`
	HistorySince     int64  `json:"historySince,omitempty"`
	HistoryUntil     int64  `json:"historyUntil,omitempty"`
	HistoryDateLocal string `json:"historyDateLocal,omitempty"`
	HistoryTimeMode  string `json:"historyTimeMode,omitempty"`
}
type SearchMatch struct {
	Slug        string   `json:"slug"`
	Scope       string   `json:"scope"`
	WorkdirHash string   `json:"workdirHash,omitempty"`
	MemoryType  string   `json:"memoryType"`
	Description string   `json:"description"`
	Headline    string   `json:"headline"`
	Snippet     string   `json:"snippet"`
	Score       float64  `json:"score"`
	RawScore    *float64 `json:"rawScore"`
	AgeDays     *float64 `json:"ageDays"`
	Unreviewed  bool     `json:"unreviewed"`
	Confidence  string   `json:"confidence"`
}
type SearchResponse struct {
	Matches        []SearchMatch `json:"matches"`
	HistoryMatches []any         `json:"historyMatches"`
	UsedFallback   bool          `json:"usedFallback"`
}
type OverviewResponse struct {
	User        []Meta  `json:"user"`
	Project     []Meta  `json:"project"`
	Global      []Meta  `json:"global"`
	RecentDays  []Meta  `json:"recentDays"`
	Root        string  `json:"root"`
	WorkdirHash *string `json:"workdirHash"`
}
type Decision struct {
	Op          string    `json:"op"`
	Slug        string    `json:"slug"`
	Scope       string    `json:"scope,omitempty"`
	WorkdirHash string    `json:"workdirHash,omitempty"`
	MemoryType  string    `json:"memoryType,omitempty"`
	Description *string   `json:"description,omitempty"`
	Body        *string   `json:"body,omitempty"`
	Mode        string    `json:"mode,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	GroupID     string    `json:"groupId,omitempty"`
	Evidence    *Evidence `json:"evidence,omitempty"`
}
type BatchArgs struct {
	Workdir        string `json:"workdir,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	Trigger        string `json:"trigger,omitempty"`
	Model          string `json:"model,omitempty"`
	LocalDate      string `json:"localDate,omitempty"`
	DailyAppend    *struct {
		Bullet string `json:"bullet"`
	} `json:"dailyAppend,omitempty"`
	Decisions []Decision `json:"decisions,omitempty"`
}
type BatchWarning struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	Slug          string `json:"slug,omitempty"`
	Op            string `json:"op,omitempty"`
	GroupID       string `json:"groupId,omitempty"`
	DecisionIndex int    `json:"decisionIndex"`
	Details       any    `json:"details"`
}
type BatchResponse struct {
	Created        []string       `json:"created"`
	Updated        []string       `json:"updated"`
	Deleted        []string       `json:"deleted"`
	Warnings       []string       `json:"warnings"`
	WarningDetails []BatchWarning `json:"warningDetails"`
}
type Rejection struct {
	Slug        string `json:"slug"`
	Scope       string `json:"scope"`
	WorkdirHash string `json:"workdirHash"`
	RejectedAt  int64  `json:"rejectedAt"`
	Actor       string `json:"actor"`
	Reason      string `json:"reason,omitempty"`
}

// OrganizeRun retains optional report fields without rewriting old report versions.
type OrganizeRun map[string]any
type storeState struct {
	Rejections     []Rejection       `json:"rejections"`
	Runs           []OrganizeRun     `json:"runs"`
	Audit          []json.RawMessage `json:"audit"`
	OrganizerEpoch uint64            `json:"organizerEpoch,omitempty"`
}

type StoreError struct {
	Code    string
	Message string
}

func (e *StoreError) Error() string         { return e.Message }
func storeError(code, message string) error { return &StoreError{Code: code, Message: message} }
