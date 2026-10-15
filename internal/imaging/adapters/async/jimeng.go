package asyncadapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const jimengKey = "jimeng_t2i_v40"

type Jimeng struct {
	transport
	akRef, skRef, tokenRef string
	now                    func() time.Time
}

var _ imaging.Adapter = (*Jimeng)(nil)

func NewJimeng(o Options) (*Jimeng, error) {
	t, e := newTransport(o, "https://visual.volcengineapi.com")
	if e != nil {
		return nil, e
	}
	if o.AccessKeyRef == "" || o.SecretKeyRef == "" {
		return nil, failure(imaging.CodeAuthRequired, imaging.DeliveryNotSent)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Jimeng{transport: t, akRef: o.AccessKeyRef, skRef: o.SecretKeyRef, tokenRef: o.SecurityTokenRef, now: o.Now}, nil
}
func (*Jimeng) Cancel(context.Context, string) error { return imaging.ErrUnsupported }
func (*Jimeng) Reconcile(context.Context, string) (imaging.RemoteResult, error) {
	return imaging.RemoteResult{}, imaging.ErrUnsupported
}

type visualResponse struct {
	Code int `json:"code"`
	Data struct {
		TaskID string   `json:"task_id"`
		Status string   `json:"status"`
		URLs   []string `json:"image_urls"`
		Base64 []string `json:"binary_data_base64"`
	} `json:"data"`
	Metadata *struct {
		Error *struct {
			Code string `json:"Code"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
}

func (a *Jimeng) call(ctx context.Context, action string, body any) (visualResponse, error) {
	var out visualResponse
	ak, e := a.secret(ctx, a.akRef)
	if e != nil {
		return out, e
	}
	sk, e := a.secret(ctx, a.skRef)
	if e != nil {
		return out, e
	}
	token := ""
	if a.tokenRef != "" {
		token, e = a.secret(ctx, a.tokenRef)
		if e != nil {
			return out, e
		}
	}
	raw, e := json.Marshal(body)
	if e != nil {
		return out, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/?Action="+action+"&Version=2022-08-31", bytes.NewReader(raw))
	if e != nil {
		return out, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent)
	}
	signVisual(req, raw, ak, sk, token, a.now())
	if e = a.do(req, &out); e != nil {
		return out, e
	}
	if out.Metadata != nil {
		if out.Metadata.Error == nil {
			return out, failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain)
		}
		code := imaging.CodeUnavailable
		switch out.Metadata.Error.Code {
		case "InvalidTimestamp", "InvalidAccessKey", "SignatureDoesNotMatch", "InvalidSecretToken", "InvalidAuthorization", "InvalidCredential":
			code = imaging.CodeAuthRequired
		case "AccessDenied":
			code = imaging.CodeForbidden
		}
		return out, failure(code, imaging.DeliveryRejected)
	}
	if out.Code != 10000 {
		code, d := imaging.CodeUnavailable, imaging.DeliveryUncertain
		switch out.Code {
		case 50400:
			code, d = imaging.CodeAuthRequired, imaging.DeliveryRejected
		case 50429, 50430:
			code, d = imaging.CodeRateLimited, imaging.DeliveryRejected
		case 50200, 50201, 50204, 50205, 50207, 50213, 50215:
			code, d = imaging.CodeInvalidInput, imaging.DeliveryRejected
		case 50411, 50412, 50413, 50518, 50511, 50519, 50512, 50520, 50521, 50522:
			code, d = imaging.CodeForbidden, imaging.DeliveryRejected
		}
		return out, failure(code, d)
	}
	return out, nil
}
func (a *Jimeng) Submit(ctx context.Context, r imaging.ExecuteRequest) (imaging.RemoteResult, error) {
	// Local assets have no trusted published URL in the current core contract.
	if len(r.Inputs) > 0 || len(r.Request.Inputs) > 0 || len(r.Continuation) > 0 || r.Request.Operation != imaging.OperationGenerate {
		return imaging.RemoteResult{}, imaging.ErrUnsupported
	}
	if r.Target.Adapter != JimengID || r.Target.AdapterVersion != Version || r.Target.RemoteID != jimengKey || r.Request.Count != 1 || strings.TrimSpace(r.Request.Prompt) == "" || len(r.Request.Prompt) > 64<<10 {
		return resultError("", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
	}
	body := map[string]any{"req_key": jimengKey, "prompt": r.Request.Prompt, "force_single": true}
	for k, v := range r.Request.Parameters {
		n, ok := number(v)
		if !ok {
			return resultError("", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
		}
		switch k {
		case "size":
			ok = n >= 1048576 && n <= 16777216 && n == math.Trunc(n)
		case "width", "height":
			ok = n > 0 && n <= 16777216 && n == math.Trunc(n)
		case "scale":
			ok = n >= 0 && n <= 1 && math.Abs(n*100-math.Round(n*100)) < 1e-8
		case "min_ratio", "max_ratio":
			ok = n >= 1.0/16 && n < 16
		default:
			ok = false
		}
		if !ok {
			return resultError("", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
		}
		body[k] = n
	}
	w, wok := body["width"].(float64)
	h, hok := body["height"].(float64)
	min, max := 1.0/3, 3.0
	if n, ok := body["min_ratio"].(float64); ok {
		min = n
	}
	if n, ok := body["max_ratio"].(float64); ok {
		max = n
	}
	if min > max || wok != hok || (wok && (w*h < 1048576 || w*h > 16777216 || w/h < min || w/h > max)) {
		return resultError("", failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
	}
	out, e := a.call(ctx, "CVSync2AsyncSubmitTask", body)
	if e != nil {
		return resultError("", e)
	}
	if !validHandle(out.Data.TaskID) {
		return resultError("", failure(imaging.CodeResultUnknown, imaging.DeliveryUncertain))
	}
	return pending(out.Data.TaskID), nil
}
func (a *Jimeng) Poll(ctx context.Context, h string) (imaging.RemoteResult, error) {
	if !validHandle(h) {
		return resultError(h, failure(imaging.CodeInvalidInput, imaging.DeliveryNotSent))
	}
	out, e := a.call(ctx, "CVSync2AsyncGetResult", map[string]any{"req_key": jimengKey, "task_id": h, "req_json": `{"return_url":true}`})
	if e != nil {
		// Only an explicit done result with a documented output rejection is terminal.
		if out.Metadata == nil && out.Data.Status == "done" {
			switch out.Code {
			case 50511, 50519, 50512, 50520, 50521, 50522:
				return imaging.RemoteResult{State: "failed", Handle: h, ErrorCode: imaging.CodeForbidden}, nil
			}
		}
		return resultError(h, e)
	}
	switch out.Data.Status {
	case "in_queue", "generating":
		return pending(h), nil
	case "done":
		outputs := []imaging.OutputSource{}
		for _, u := range out.Data.URLs {
			if !validOutputURL(u) {
				return unknown(h, imaging.CodeResultUnknown), nil
			}
			outputs = append(outputs, imaging.OutputSource{Kind: "url", MIME: "image/png", URL: u})
		}
		// return_url=true normally selects URLs. Do not duplicate URL/base64 variants.
		if len(outputs) == 0 {
			for _, s := range out.Data.Base64 {
				data, err := base64.StdEncoding.Strict().DecodeString(s)
				if err != nil || len(data) == 0 {
					return unknown(h, imaging.CodeResultUnknown), nil
				}
				validated, err := imaging.DecodeImageContent(ctx, bytes.NewReader(data), "image/png", imaging.ImageLimits{MaxBytes: maxResponse, MaxPixels: 100_000_000})
				if err != nil {
					return unknown(h, imaging.CodeResultUnknown), nil
				}
				outputs = append(outputs, imaging.OutputSource{Kind: "bytes", MIME: "image/png", Data: validated.Data})
			}
		}
		if len(outputs) == 0 {
			return unknown(h, imaging.CodeNoImageOutput), nil
		}
		return imaging.RemoteResult{State: "completed", Handle: h, Outputs: outputs}, nil
	default:
		return unknown(h, imaging.CodeResultUnknown), nil
	}
}
func number(v any) (float64, bool) {
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case int64:
		n = float64(x)
	case json.Number:
		var e error
		n, e = x.Float64()
		if e != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}
