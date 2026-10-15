package asyncadapter

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const RunningHubModelID = "runninghub-model"
const RunningHubLiteModel = "rhart-image-n-g31-flash-lite-official"

// Model targets use documented V2 model endpoints. They do not consume a
// workflow graph, node mappings, or the workflow webhook-detail protocol.
type RunningHubModel struct {
	transport
	secretRef string
}

var _ imaging.Adapter = (*RunningHubModel)(nil)

func NewRunningHubModel(o Options) (*RunningHubModel, error) {
	t, err := newTransport(o, "https://www.runninghub.cn")
	if err != nil {
		return nil, err
	}
	if o.SecretRef == "" {
		return nil, failure(imaging.CodeAuthRequired, imaging.DeliveryNotSent)
	}
	return &RunningHubModel{transport: t, secretRef: o.SecretRef}, nil
}

type modelResult struct {
	TaskID    string          `json:"taskId"`
	Status    string          `json:"status"`
	ErrorCode json.RawMessage `json:"errorCode"`
	Results   []struct {
		URL  string `json:"url"`
		Type string `json:"outputType"`
	} `json:"results"`
}

func modelErrorCode(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var code string
	if json.Unmarshal(raw, &code) != nil {
		code = string(raw)
	}
	if code == "0" {
		return ""
	}
	return code
}
func modelRejection(code string) error {
	switch code {
	case "1001", "1007", "1009":
		return failure(imaging.CodeInvalidInput, imaging.DeliveryRejected)
	case "1002":
		return failure(imaging.CodeAuthRequired, imaging.DeliveryRejected)
	case "1003", "1520":
		return failure(imaging.CodeRateLimited, imaging.DeliveryRejected)
	case "1008":
		return failure(imaging.CodeTooLarge, imaging.DeliveryRejected)
	case "1014", "1501", "1505":
		return failure(imaging.CodeForbidden, imaging.DeliveryRejected)
	default:
		return failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	}
}
func decodeModelResult(r modelResult, expected string) imaging.RemoteResult {
	if !validHandle(r.TaskID) || (expected != "" && r.TaskID != expected) {
		return unknown(expected, imaging.CodeResultUnknown)
	}
	if modelErrorCode(r.ErrorCode) != "" {
		return imaging.RemoteResult{State: "failed", Handle: r.TaskID, ErrorCode: imaging.CodeUnavailable}
	}
	switch r.Status {
	case "QUEUED", "RUNNING":
		return pending(r.TaskID)
	case "FAILED":
		return imaging.RemoteResult{State: "failed", Handle: r.TaskID, ErrorCode: imaging.CodeUnavailable}
	case "SUCCESS":
		if len(r.Results) != 1 {
			return unknown(r.TaskID, imaging.CodeNoImageOutput)
		}
		item := r.Results[0]
		mime := imageMIME(item.Type)
		if mime == "" || !validOutputURL(item.URL) {
			return unknown(r.TaskID, imaging.CodeResultUnknown)
		}
		return imaging.RemoteResult{State: "completed", Handle: r.TaskID, Outputs: []imaging.OutputSource{{Kind: "url", URL: item.URL, MIME: mime}}}
	default:
		return unknown(r.TaskID, imaging.CodeResultUnknown)
	}
}

func (a *RunningHubModel) Submit(ctx context.Context, r imaging.ExecuteRequest) (imaging.RemoteResult, error) {
	if r.Target.Adapter != RunningHubModelID || r.Target.AdapterVersion != Version || r.Target.Kind != "model" || r.Target.RemoteID != RunningHubLiteModel || r.Target.WorkflowMapping != nil || r.Request.Operation != imaging.OperationGenerate || r.Request.Count != 1 || len(r.Request.Inputs) > 0 || len(r.Inputs) > 0 || len(r.Continuation) > 0 || len(r.Request.Parameters) > 0 || !utf8.ValidString(r.Request.Prompt) || strings.TrimSpace(r.Request.Prompt) == "" || utf8.RuneCountInString(r.Request.Prompt) > 20000 {
		return resultError("", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
	}
	key, err := a.secret(ctx, a.secretRef)
	if err != nil {
		return resultError("", err)
	}
	var out modelResult
	err = a.bearerJSON(ctx, "/openapi/v2/"+RunningHubLiteModel+"/text-to-image", key, map[string]any{"prompt": r.Request.Prompt, "aspectRatio": "1:1"}, &out)
	if err != nil {
		return resultError("", err)
	}
	if code := modelErrorCode(out.ErrorCode); code != "" && out.TaskID == "" {
		return resultError("", modelRejection(code))
	}
	return decodeModelResult(out, ""), nil
}
func (a *RunningHubModel) Poll(ctx context.Context, handle string) (imaging.RemoteResult, error) {
	if !validHandle(handle) {
		return resultError("", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
	}
	key, err := a.secret(ctx, a.secretRef)
	if err != nil {
		return resultError(handle, err)
	}
	var out modelResult
	err = a.bearerJSON(ctx, "/openapi/v2/query", key, map[string]any{"taskId": handle}, &out)
	if err != nil {
		return resultError(handle, err)
	}
	return decodeModelResult(out, handle), nil
}
func (*RunningHubModel) Cancel(context.Context, string) error { return imaging.ErrUnsupported }
func (*RunningHubModel) Reconcile(context.Context, string) (imaging.RemoteResult, error) {
	return imaging.RemoteResult{}, imaging.ErrUnsupported
}
