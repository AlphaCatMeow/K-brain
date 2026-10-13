package imaging

import (
	"encoding/json"
	"math"
	"testing"
)

func testRequest() CreateJobRequest {
	return CreateJobRequest{ProjectID: "ip_test", TargetID: "it_test", Operation: OperationGenerate, Prompt: "海报", Count: 1, Parameters: map[string]any{"quality": "high", "steps": 5}}
}
func TestHashRequestOrder(t *testing.T) {
	a := testRequest()
	b := a
	b.Parameters = map[string]any{"steps": json.Number("5.0"), "quality": "high"}
	ha, err := HashRequest(a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HashRequest(b)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatal("map order or number representation changed hash")
	}
	b.Prompt = "different"
	hb, _ = HashRequest(b)
	if ha == hb {
		t.Fatal("prompt excluded from hash")
	}
	a.Parameters = map[string]any{"ordered": []any{1, 2}}
	b = a
	b.Parameters = map[string]any{"ordered": []any{2, 1}}
	ha, _ = HashRequest(a)
	hb, _ = HashRequest(b)
	if ha == hb {
		t.Fatal("array order lost")
	}
	a = testRequest()
	b = a
	b.Inputs = []InputAsset{}
	ha, _ = HashRequest(a)
	hb, _ = HashRequest(b)
	if ha != hb {
		t.Fatal("empty inputs not canonical")
	}
	for _, v := range []any{math.NaN(), math.Inf(1), math.Inf(-1), json.Number("NaN"), []any{math.NaN()}, map[string]any{"nested": math.Inf(1)}} {
		a.Parameters = map[string]any{"value": v}
		if _, err := HashRequest(a); err == nil {
			t.Fatalf("accepted non-finite %v", v)
		}
	}
}
func TestRequestValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*CreateJobRequest)
		valid  bool
	}{
		{"valid", func(r *CreateJobRequest) {}, true},
		{"unknown parameter", func(r *CreateJobRequest) { r.Parameters["secret"] = "sentinel" }, false},
		{"wrong type", func(r *CreateJobRequest) { r.Parameters["steps"] = "5" }, false},
		{"fraction integer", func(r *CreateJobRequest) { r.Parameters["steps"] = 5.5 }, false},
		{"exact fraction integer", func(r *CreateJobRequest) { r.Parameters["steps"] = json.Number("5.00000000000000001") }, false},
		{"exact maximum", func(r *CreateJobRequest) { r.Parameters["steps"] = json.Number("10.00000000000000001") }, false},
		{"duplicate assets", func(r *CreateJobRequest) {
			r.Operation = OperationReference
			r.Inputs = []InputAsset{{AssetID: "ia_same", Role: "reference"}, {AssetID: "ia_same", Role: "reference"}}
		}, false},
		{"input limit", func(r *CreateJobRequest) {
			r.Operation = OperationReference
			r.Inputs = []InputAsset{{AssetID: "ia_a", Role: "reference"}, {AssetID: "ia_b", Role: "reference"}, {AssetID: "ia_c", Role: "reference"}, {AssetID: "ia_d", Role: "reference"}, {AssetID: "ia_e", Role: "reference"}}
		}, false},
		{"missing required", func(r *CreateJobRequest) { delete(r.Parameters, "steps") }, false},
		{"enum", func(r *CreateJobRequest) { r.Parameters["quality"] = "ultra" }, false},
		{"range", func(r *CreateJobRequest) { r.Parameters["steps"] = 11 }, false},
		{"zero count", func(r *CreateJobRequest) { r.Count = 0 }, false},
		{"count limit", func(r *CreateJobRequest) { r.Count = 5 }, false},
		{"operation", func(r *CreateJobRequest) { r.Operation = "other" }, false},
		{"target mismatch", func(r *CreateJobRequest) { r.TargetID = "it_other" }, false},
		{"generate input", func(r *CreateJobRequest) { r.Inputs = []InputAsset{{AssetID: "ia_source", Role: "source"}} }, false},
		{"reference", func(r *CreateJobRequest) {
			r.Operation = OperationReference
			r.Inputs = []InputAsset{{AssetID: "ia_ref", Role: "reference"}}
		}, true},
		{"reference missing", func(r *CreateJobRequest) { r.Operation = OperationReference }, false},
		{"edit", func(r *CreateJobRequest) {
			r.Operation = OperationEdit
			r.Inputs = []InputAsset{{AssetID: "ia_src", Role: "source"}}
		}, true},
		{"mask pair", func(r *CreateJobRequest) {
			r.Operation = OperationMask
			r.Inputs = []InputAsset{{AssetID: "ia_src", Role: "source"}, {AssetID: "ia_mask", Role: "mask"}}
		}, true},
		{"mask no source", func(r *CreateJobRequest) {
			r.Operation = OperationMask
			r.Inputs = []InputAsset{{AssetID: "ia_mask", Role: "mask"}}
		}, false},
		{"mask in edit", func(r *CreateJobRequest) {
			r.Operation = OperationEdit
			r.Inputs = []InputAsset{{AssetID: "ia_src", Role: "source"}, {AssetID: "ia_mask", Role: "mask"}}
		}, false},
		{"unknown role", func(r *CreateJobRequest) {
			r.Operation = OperationReference
			r.Inputs = []InputAsset{{AssetID: "ia_ref", Role: "url"}}
		}, false},
		{"path injection", func(r *CreateJobRequest) {
			r.Operation = OperationReference
			r.Inputs = []InputAsset{{AssetID: "/tmp/private.png", Role: "reference"}}
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRequest()
			tc.change(&r)
			err := ValidateRequest(r, SnapshotTarget(testTarget()))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
