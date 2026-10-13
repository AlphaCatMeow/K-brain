package asyncadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// WorkflowOptions freezes a locally reviewed API-format graph and mapping.
// No workflow or operation is supplied by default. WebhookURL enables only the
// documented candidate authenticated event-detail path, not production evidence.
// OutputName must be "image" (a local label, never an inferred upstream socket).
// Input ParameterKey supports prompt, count, declared parameters, and input.<role>.
type WorkflowOptions struct {
	Options
	Target        imaging.TargetSnapshot
	APIFormatJSON []byte
	WebhookURL    string
}
type RunningHub struct {
	transport
	secretRef  string
	target     imaging.TargetSnapshot
	graph      map[string]workflowNode
	digest     string
	webhookURL string
}
type workflowNode struct {
	Inputs    map[string]json.RawMessage `json:"inputs"`
	ClassType string                     `json:"class_type"`
}
type rhEnvelope struct {
	Code *int            `json:"code"`
	Data json.RawMessage `json:"data"`
}
type nodeValue struct {
	NodeID     string `json:"nodeId"`
	FieldName  string `json:"fieldName"`
	FieldValue string `json:"fieldValue"`
}

var _ imaging.Adapter = (*RunningHub)(nil)

func graphDecode(raw []byte) (map[string]workflowNode, string, error) {
	if len(raw) == 0 || len(raw) > maxResponse {
		return nil, "", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	var g map[string]workflowNode
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if json.Unmarshal(raw, &g) != nil || len(g) == 0 || dec.Decode(&generic) != nil {
		return nil, "", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	canonical, e := json.Marshal(generic)
	if e != nil {
		return nil, "", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	return g, hash(canonical), nil
}
func NewRunningHub(o WorkflowOptions) (*RunningHub, error) {
	t, e := newTransport(o.Options, "https://www.runninghub.cn")
	if e != nil {
		return nil, e
	}
	g, d, e := graphDecode(o.APIFormatJSON)
	if e != nil {
		return nil, e
	}
	s := o.Target
	m := s.WorkflowMapping
	if o.SecretRef == "" || s.Adapter != RunningHubID || s.AdapterVersion != Version || s.Kind != "workflow" || !validHandle(s.RemoteID) || s.Revision == 0 || m == nil || m.Revision == 0 || len(m.Outputs) == 0 {
		return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	if o.WebhookURL != "" && !validOutputURL(o.WebhookURL) {
		return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	seen := map[string]bool{}
	bound := map[string]bool{}
	for _, in := range m.Inputs {
		v, ok := g[in.NodeID].Inputs[in.InputName]
		if !ok || len(v) == 0 || v[0] == '[' || v[0] == '{' || in.ParameterKey == "" || in.ParameterKey == "input." || bound[in.ParameterKey] || seen[in.NodeID+"\x00"+in.InputName] {
			return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		}
		seen[in.NodeID+"\x00"+in.InputName] = true
		bound[in.ParameterKey] = true
		declared := false
		for _, p := range s.Parameters {
			if p.Key == in.ParameterKey {
				declared = true
				if p.Key == "prompt" || strings.HasPrefix(p.Key, "input.") {
					var original string
					if p.Type != "string" || p.Required || p.Default != nil || json.Unmarshal(v, &original) != nil {
						return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
					}
				}
				if p.Key == "count" && (p.Type != "integer" || p.Required || p.Default != nil) {
					return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
				}
			}
		}
		if !declared {
			return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		}
	}
	for _, p := range s.Parameters {
		if !bound[p.Key] {
			return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		}
	}
	seen = map[string]bool{}
	for _, out := range m.Outputs {
		if _, ok := g[out.NodeID]; !ok || out.OutputName != "image" || seen[out.NodeID] {
			return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
		}
		seen[out.NodeID] = true
	}
	// Freeze mutable slices/maps independently of caller-owned configuration.
	raw, _ := json.Marshal(s)
	var frozen imaging.TargetSnapshot
	if json.Unmarshal(raw, &frozen) != nil {
		return nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	return &RunningHub{transport: t, secretRef: o.SecretRef, target: frozen, graph: g, digest: d, webhookURL: o.WebhookURL}, nil
}
func rhError(code int) error {
	c, d := imaging.CodeUnavailable, imaging.DeliveryUncertain
	switch code {
	case 301, 380, 433, 803, 810, 1007, 1101:
		c, d = imaging.CodeInvalidInput, imaging.DeliveryRejected
	case 401, 801, 802, 811, 1002:
		c, d = imaging.CodeAuthRequired, imaging.DeliveryRejected
	case 416, 812, 1501:
		c, d = imaging.CodeForbidden, imaging.DeliveryRejected
	case 415, 421, 1003:
		c, d = imaging.CodeRateLimited, imaging.DeliveryRejected
	case 807, 423, 1004:
		c = imaging.CodeResultUnknown
	}
	return failure(c, d)
}
func (a *RunningHub) legacy(ctx context.Context, path, key string, body map[string]any, dst any) error {
	body["apiKey"] = key
	var out rhEnvelope
	if e := a.bearerJSON(ctx, path, key, body, &out); e != nil {
		return e
	}
	if out.Code == nil {
		return failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	}
	if *out.Code != 0 {
		return rhError(*out.Code)
	}
	if dst != nil && json.Unmarshal(out.Data, dst) != nil {
		return failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	}
	return nil
}
func (a *RunningHub) verifyGraph(ctx context.Context, key string) error {
	var out struct {
		Prompt string `json:"prompt"`
	}
	if e := a.legacy(ctx, "/api/openapi/getJsonApiFormat", key, map[string]any{"workflowId": a.target.RemoteID}, &out); e != nil {
		return e
	}
	_, d, e := graphDecode([]byte(out.Prompt))
	if e != nil || d != a.digest {
		return failure(imaging.CodeRevisionConflict, imaging.DeliveryNotSent)
	}
	return nil
}
func (a *RunningHub) Submit(ctx context.Context, r imaging.ExecuteRequest) (imaging.RemoteResult, error) {
	if !sameTarget(r.Target, a.target) {
		return resultError("", failure(imaging.CodeRevisionConflict, imaging.DeliveryNotSent))
	}
	values, assets, e := a.values(r)
	if e != nil {
		return resultError("", e)
	}
	key, e := a.secret(ctx, a.secretRef)
	if e != nil {
		return resultError("", e)
	}
	// A failure here cannot mean the generation submit was delivered.
	if e = a.verifyGraph(ctx, key); e != nil {
		return resultError("", beforeSubmit(e))
	}
	for i, asset := range assets {
		if asset != nil {
			name, err := a.upload(ctx, key, *asset)
			if err != nil {
				return resultError("", beforeSubmit(err))
			}
			values[i].FieldValue = name
		}
	}
	body := map[string]any{"workflowId": a.target.RemoteID, "nodeInfoList": values}
	if a.webhookURL != "" {
		body["webhookUrl"] = a.webhookURL
	}
	var out struct {
		TaskID json.RawMessage `json:"taskId"`
		Status string          `json:"taskStatus"`
	}
	if e = a.legacy(ctx, "/task/openapi/create", key, body, &out); e != nil {
		return resultError("", e)
	}
	h := opaqueID(out.TaskID)
	if !validHandle(h) {
		return resultError("", failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain))
	}
	switch out.Status {
	case "QUEUED", "RUNNING":
		return pending(h), nil
	case "FAILED":
		return imaging.RemoteResult{State: "failed", Handle: h, ErrorCode: imaging.CodeUnavailable}, nil
	default:
		return unknown(h, imaging.CodeResultUnknown), nil
	}
}
func beforeSubmit(e error) error {
	if x, ok := e.(*imaging.AdapterError); ok {
		return failure(x.Code, imaging.DeliveryNotSent)
	}
	return failure(imaging.CodeUnavailable, imaging.DeliveryNotSent)
}
func sameTarget(a, b imaging.TargetSnapshot) bool {
	ar, e := json.Marshal(a)
	br, f := json.Marshal(b)
	return e == nil && f == nil && bytes.Equal(ar, br)
}
func opaqueID(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	s = string(raw)
	if s == "" {
		return ""
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return s
}
func (a *RunningHub) values(r imaging.ExecuteRequest) ([]nodeValue, []*imaging.ResolvedAsset, error) {
	bad := func() ([]nodeValue, []*imaging.ResolvedAsset, error) {
		return nil, nil, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	allowed := false
	for _, op := range a.target.Capabilities.Operations {
		if op == r.Request.Operation {
			allowed = true
		}
	}
	if !allowed || r.Request.Operation == imaging.OperationMask || len(r.Continuation) > 0 || r.Request.Count < 1 || r.Request.Count > a.target.Capabilities.MaxCount || len(r.Inputs) != len(r.Request.Inputs) || len(r.Inputs) > a.target.Capabilities.MaxInputs {
		return bad()
	}
	declared := map[string]imaging.ParameterSpec{}
	for _, p := range a.target.Parameters {
		declared[p.Key] = p
	}
	for k, v := range r.Request.Parameters {
		if k == "prompt" || k == "count" || strings.HasPrefix(k, "input.") {
			return bad() // Reserved bindings come from typed request fields/assets only.
		}
		p, ok := declared[k]
		if !ok || !validParameter(p, v) {
			return bad()
		}
	}
	for _, p := range a.target.Parameters {
		if p.Required {
			if _, ok := r.Request.Parameters[p.Key]; !ok {
				return bad()
			}
		}
	}
	roles := map[string]*imaging.ResolvedAsset{}
	for i, in := range r.Request.Inputs {
		if in.AssetID != r.Inputs[i].AssetID || in.Role == "" || roles[in.Role] != nil {
			return bad()
		}
		roles[in.Role] = &r.Inputs[i]
	}
	mapped := map[string]bool{}
	usedRoles := map[string]bool{}
	values := []nodeValue{}
	assets := []*imaging.ResolvedAsset{}
	for _, in := range a.target.WorkflowMapping.Inputs {
		mapped[in.ParameterKey] = true
		var v any
		var asset *imaging.ResolvedAsset
		switch {
		case in.ParameterKey == "prompt":
			v = r.Request.Prompt
			if strings.TrimSpace(r.Request.Prompt) == "" {
				return bad()
			}
		case in.ParameterKey == "count":
			v = r.Request.Count
		case strings.HasPrefix(in.ParameterKey, "input."):
			role := strings.TrimPrefix(in.ParameterKey, "input.")
			asset = roles[role]
			if asset == nil {
				return bad()
			}
			usedRoles[role] = true
		default:
			var ok bool
			v, ok = r.Request.Parameters[in.ParameterKey]
			if !ok {
				continue
			}
		}
		value := ""
		if asset == nil {
			var ok bool
			value, ok = scalarString(v, a.graph[in.NodeID].Inputs[in.InputName])
			if !ok {
				return bad()
			}
		}
		values = append(values, nodeValue{in.NodeID, in.InputName, value})
		assets = append(assets, asset)
	}
	for k := range r.Request.Parameters {
		if !mapped[k] {
			return bad()
		}
	}
	if (r.Request.Prompt != "" && !mapped["prompt"]) || (r.Request.Count != 1 && !mapped["count"]) || len(usedRoles) != len(roles) {
		return bad()
	}
	return values, assets, nil
}
func validParameter(p imaging.ParameterSpec, v any) bool {
	switch p.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return false
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return false
		}
	case "integer", "number":
		n, ok := number(v)
		if !ok || (p.Type == "integer" && n != float64(int64(n))) || (p.Minimum != nil && n < *p.Minimum) || (p.Maximum != nil && n > *p.Maximum) {
			return false
		}
	default:
		return false
	}
	if len(p.Enum) > 0 {
		for _, x := range p.Enum {
			if reflect.DeepEqual(x, v) {
				return true
			}
			xn, xok := number(x)
			vn, vok := number(v)
			if xok && vok && xn == vn {
				return true
			}
		}
		return false
	}
	return true
}
func scalarString(v any, original json.RawMessage) (string, bool) {
	var prior any
	d := json.NewDecoder(bytes.NewReader(original))
	d.UseNumber()
	if d.Decode(&prior) != nil {
		return "", false
	}
	switch prior.(type) {
	case string:
		s, ok := v.(string)
		return s, ok
	case bool:
		b, ok := v.(bool)
		return strconv.FormatBool(b), ok
	case json.Number:
		n, ok := number(v)
		return strconv.FormatFloat(n, 'f', -1, 64), ok
	default:
		return "", false
	}
}
func (a *RunningHub) upload(ctx context.Context, key string, asset imaging.ResolvedAsset) (string, error) {
	if asset.Open == nil || asset.Bytes < 0 || asset.Bytes > maxUpload || imageMIME(asset.MIME) == "" {
		return "", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	stream, e := asset.Open()
	if e != nil || stream == nil {
		return "", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	image, e := imaging.DecodeImageContent(ctx, stream, asset.MIME, imaging.ImageLimits{MaxBytes: maxUpload, MaxPixels: 40_000_000})
	_ = stream.Close()
	if e != nil {
		return "", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}[image.MIME]
	part, e := mw.CreateFormFile("file", "input."+ext)
	if e != nil {
		return "", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	_, _ = part.Write(image.Data)
	_ = mw.Close()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/openapi/v2/media/upload/binary", &b)
	if e != nil {
		return "", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out rhEnvelope
	if e = a.do(req, &out); e != nil {
		return "", e
	}
	if out.Code == nil {
		return "", failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	}
	if *out.Code != 0 {
		return "", rhError(*out.Code)
	}
	var data struct {
		FileName string `json:"fileName"`
	}
	if json.Unmarshal(out.Data, &data) != nil || !validHandle(data.FileName) {
		return "", failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
	}
	return data.FileName, nil
}
func (*RunningHub) Reconcile(context.Context, string) (imaging.RemoteResult, error) {
	return imaging.RemoteResult{}, imaging.ErrUnsupported
}
func (a *RunningHub) Cancel(ctx context.Context, h string) error {
	if !validHandle(h) {
		return failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	key, e := a.secret(ctx, a.secretRef)
	if e != nil {
		return e
	}
	if e = a.legacy(ctx, "/task/openapi/cancel", key, map[string]any{"taskId": h}, nil); e != nil {
		return e
	}
	// The current scheduler interprets nil as upstream-confirmed cancellation.
	// Official evidence proves only ACK, so never report a confirmed terminal.
	return failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
}
func (a *RunningHub) Poll(ctx context.Context, h string) (imaging.RemoteResult, error) {
	if !validHandle(h) {
		return resultError(h, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
	}
	key, e := a.secret(ctx, a.secretRef)
	if e != nil {
		return resultError(h, e)
	}
	var out struct {
		TaskID json.RawMessage `json:"taskId"`
		Status string          `json:"status"`
	}
	if e = a.bearerJSON(ctx, "/openapi/v2/query", key, map[string]any{"taskId": h}, &out); e != nil {
		return resultError(h, e)
	}
	if opaqueID(out.TaskID) != h {
		return unknown(h, imaging.CodeResultUnknown), nil
	}
	switch out.Status {
	case "QUEUED", "RUNNING":
		return pending(h), nil
	case "FAILED":
		return imaging.RemoteResult{State: "failed", Handle: h, ErrorCode: imaging.CodeUnavailable}, nil
	case "SUCCESS":
		if a.webhookURL == "" {
			return unknown(h, imaging.CodeResultUnknown), nil
		}
		return a.eventOutputs(ctx, key, h)
	default:
		return unknown(h, imaging.CodeResultUnknown), nil
	}
}
func (a *RunningHub) eventOutputs(ctx context.Context, key, h string) (imaging.RemoteResult, error) {
	var detail struct {
		TaskID    json.RawMessage `json:"taskId"`
		EventData string          `json:"eventData"`
	}
	if e := a.legacy(ctx, "/task/openapi/getWebhookDetail", key, map[string]any{"taskId": h}, &detail); e != nil {
		return resultError(h, e)
	}
	if opaqueID(detail.TaskID) != h || len(detail.EventData) > maxResponse {
		return unknown(h, imaging.CodeResultUnknown), nil
	}
	var event struct {
		Code *int `json:"code"`
		Data []struct {
			NodeID string `json:"nodeId"`
			URL    string `json:"fileUrl"`
			Type   string `json:"fileType"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(detail.EventData), &event) != nil || event.Code == nil || *event.Code != 0 {
		return unknown(h, imaging.CodeResultUnknown), nil
	}
	allowed := map[string]bool{}
	for _, o := range a.target.WorkflowMapping.Outputs {
		allowed[o.NodeID] = true
	}
	outputs := []imaging.OutputSource{}
	for _, v := range event.Data {
		if !allowed[v.NodeID] {
			continue
		}
		mime := imageMIME(v.Type)
		if mime == "" || !validOutputURL(v.URL) {
			continue
		}
		outputs = append(outputs, imaging.OutputSource{Kind: "url", MIME: mime, URL: v.URL})
	}
	if len(outputs) == 0 {
		return unknown(h, imaging.CodeNoImageOutput), nil
	}
	return imaging.RemoteResult{State: "completed", Handle: h, Outputs: outputs}, nil
}
