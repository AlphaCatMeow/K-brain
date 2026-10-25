package backend

import "github.com/Stack-Cairn/K-brain/internal/protocol"

type queueEntry struct {
	ID                string                 `json:"id"`
	Request           protocol.PromptRequest `json:"request"`
	Source            string                 `json:"source"`
	CreatedAt         int64                  `json:"createdAt"`
	DraftJSON         string                 `json:"draftJson"`
	UploadedFilesJSON string                 `json:"uploadedFilesJson"`
	// MemoryOff records that the message was queued while memory was off; it stays out
	// of extraction even when it runs after memory is back on.
	MemoryOff bool `json:"memoryOff,omitempty"`
}

type queueReceipt struct {
	ID    string                `json:"id"`
	Hash  string                `json:"hash"`
	State string                `json:"state"`
	Error string                `json:"error,omitempty"`
	Run   *protocol.RunAccepted `json:"run,omitempty"`
}

type queueEdit struct {
	Entry queueEntry `json:"entry"`
	Index int        `json:"index"`
}

type queueDocument struct {
	Version  int                     `json:"version"`
	Revision uint64                  `json:"revision"`
	Paused   bool                    `json:"paused"`
	Items    []queueEntry            `json:"items"`
	Edits    map[string]queueEdit    `json:"edits"`
	Receipts map[string]queueReceipt `json:"receipts"`
}

type queueItem struct {
	ID                string                  `json:"id"`
	ClientRequestID   string                  `json:"clientRequestId"`
	PreviewText       string                  `json:"previewText"`
	FileCount         int                     `json:"fileCount"`
	CreatedAt         int64                   `json:"createdAt"`
	Source            string                  `json:"source"`
	Editable          bool                    `json:"editable"`
	Request           *protocol.PromptRequest `json:"request,omitempty"`
	DraftJSON         string                  `json:"draftJson,omitempty"`
	UploadedFilesJSON string                  `json:"uploadedFilesJson,omitempty"`
}

type queueSnapshot struct {
	ConversationID string      `json:"conversationId"`
	Revision       uint64      `json:"revision"`
	Paused         bool        `json:"paused"`
	Items          []queueItem `json:"items"`
}

type queueResponse struct {
	Version   string        `json:"version"`
	Accepted  bool          `json:"accepted"`
	ErrorCode string        `json:"errorCode,omitempty"`
	Message   string        `json:"message,omitempty"`
	Snapshot  queueSnapshot `json:"snapshot"`
	Item      *queueItem    `json:"item,omitempty"`
	Receipt   *queueReceipt `json:"receipt,omitempty"`
}

type queueSubmit struct {
	Request           protocol.PromptRequest `json:"request"`
	Policy            string                 `json:"policy,omitempty"`
	Source            string                 `json:"source,omitempty"`
	DraftJSON         string                 `json:"draft_json,omitempty"`
	UploadedFilesJSON string                 `json:"uploaded_files_json,omitempty"`
}

type queueAction struct {
	Action            string                  `json:"action"`
	ItemID            string                  `json:"item_id,omitempty"`
	Revision          uint64                  `json:"revision"`
	Direction         string                  `json:"direction,omitempty"`
	Request           *protocol.PromptRequest `json:"request,omitempty"`
	DraftJSON         string                  `json:"draft_json,omitempty"`
	UploadedFilesJSON string                  `json:"uploaded_files_json,omitempty"`
}
