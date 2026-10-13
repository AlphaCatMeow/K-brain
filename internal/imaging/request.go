package imaging

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
)

// NormalizeRequest preserves text and array order, sorts object keys, and uses
// one exact decimal spelling for numbers. Empty inputs/parameters have one form.
func NormalizeRequest(r CreateJobRequest) ([]byte, error) {
	if r.Inputs == nil {
		r.Inputs = []InputAsset{}
	}
	if r.Parameters == nil {
		r.Parameters = map[string]any{}
	}
	return canonicalValue(r)
}
func HashRequest(r CreateJobRequest) (string, error) {
	b, err := NormalizeRequest(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
func canonicalValue(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, invalid("request contains a non-JSON value")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err = d.Decode(&value); err != nil {
		return nil, invalid("invalid request JSON")
	}
	value, err = canonicalNumbers(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func canonicalNumbers(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		s, err := decimalNumber(string(x))
		if err != nil {
			return nil, err
		}
		return json.Number(s), nil
	case []any:
		for i, n := range x {
			c, err := canonicalNumbers(n)
			if err != nil {
				return nil, err
			}
			x[i] = c
		}
		return x, nil
	case map[string]any:
		for k, n := range x {
			c, err := canonicalNumbers(n)
			if err != nil {
				return nil, err
			}
			x[k] = c
		}
		return x, nil
	default:
		return v, nil
	}
}

// JSON has finite decimals. Expanding bounded exponents preserves large integer
// identity rather than rounding every value through float64.
func decimalNumber(s string) (string, error) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return "", invalid("non-finite numeric value")
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err = strconv.Atoi(s[i+1:])
		if err != nil || exp < -10000 || exp > 10000 {
			return "", invalid("numeric exponent is out of range")
		}
		s = s[:i]
	}
	frac := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		frac = len(s) - i - 1
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		return "0", nil
	}
	pos := len(s) + exp - frac
	if pos <= 0 {
		s = "0." + strings.Repeat("0", -pos) + s
	} else if pos >= len(s) {
		s += strings.Repeat("0", pos-len(s))
	} else {
		s = s[:pos] + "." + s[pos:]
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if neg {
		s = "-" + s
	}
	return s, nil
}
func numeric(v any) (float64, bool) {
	if n, ok := v.(json.Number); ok {
		f, err := strconv.ParseFloat(string(n), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return 0, false
	}
	var f float64
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f = float64(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f = float64(rv.Uint())
	case reflect.Float32, reflect.Float64:
		f = rv.Float()
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}
func exactNumeric(v any) (*big.Rat, bool) {
	if _, ok := numeric(v); !ok {
		return nil, false
	}
	raw, err := canonicalValue(v)
	if err != nil {
		return nil, false
	}
	n, ok := new(big.Rat).SetString(string(raw))
	return n, ok
}
func validateParameter(v any, p ParameterSpec, checkEnum bool) error {
	switch p.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return invalid("parameter requires string")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return invalid("parameter requires boolean")
		}
	case "number", "integer":
		n, ok := exactNumeric(v)
		if !ok || (p.Type == "integer" && !n.IsInt()) {
			return invalid("parameter requires finite numeric type")
		}
		if p.Minimum != nil {
			minimum, valid := exactNumeric(*p.Minimum)
			if !valid || n.Cmp(minimum) < 0 {
				return invalid("parameter outside schema range")
			}
		}
		if p.Maximum != nil {
			maximum, valid := exactNumeric(*p.Maximum)
			if !valid || n.Cmp(maximum) > 0 {
				return invalid("parameter outside schema range")
			}
		}
	default:
		return invalid("unsupported parameter type")
	}
	if checkEnum && p.Enum != nil {
		raw, err := canonicalValue(v)
		if err != nil {
			return err
		}
		found := false
		for _, e := range p.Enum {
			other, err := canonicalValue(e)
			if err == nil && bytes.Equal(raw, other) {
				found = true
				break
			}
		}
		if !found {
			return invalid("parameter outside declared enum")
		}
	}
	return nil
}

// ValidateRequest performs structural checks against a frozen target. Asset
// ownership, decoded dimensions and mask transparency require the asset stage.
func ValidateRequest(r CreateJobRequest, t TargetSnapshot) error {
	if !validID(r.ProjectID, "ip_") || !validID(r.TargetID, "it_") || r.TargetID != t.ID {
		return invalid("invalid project or target identity")
	}
	if r.ParentAssetID != "" && !validID(r.ParentAssetID, "ia_") {
		return invalid("invalid parent asset identity")
	}
	if !validOperation(r.Operation) {
		return invalid("unknown operation")
	}
	supported := false
	for _, op := range t.Capabilities.Operations {
		if op == r.Operation {
			supported = true
			break
		}
	}
	if !supported {
		return &ImageError{Code: CodeUnsupportedOperation}
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return invalid("prompt is required")
	}
	if r.Count < 1 || r.Count > 16 || r.Count > t.Capabilities.MaxCount {
		return invalid("request count exceeds target limit")
	}
	if len(r.Inputs) > 16 || len(r.Inputs) > t.Capabilities.MaxInputs {
		return invalid("request inputs exceed target limit")
	}
	if err := validateCapabilities(t.Capabilities); err != nil {
		return err
	}
	if err := validateSchema(t.Parameters); err != nil {
		return err
	}
	if err := validateWorkflow(t.Kind, t.WorkflowMapping, t.Parameters); err != nil {
		return err
	}
	specs := map[string]ParameterSpec{}
	for _, p := range t.Parameters {
		specs[p.Key] = p
	}
	for k, v := range r.Parameters {
		p, ok := specs[k]
		if !ok {
			return invalid("unknown parameter")
		}
		if err := validateParameter(v, p, true); err != nil {
			return err
		}
	}
	for _, p := range t.Parameters {
		if _, ok := r.Parameters[p.Key]; !ok && p.Required && p.Default == nil {
			return invalid("required parameter missing")
		}
	}
	roles := map[string]int{}
	ids := map[string]bool{}
	for _, in := range r.Inputs {
		if !validID(in.AssetID, "ia_") || ids[in.AssetID] {
			return invalid("invalid or duplicate input asset")
		}
		ids[in.AssetID] = true
		switch in.Role {
		case "source", "reference", "mask":
			roles[in.Role]++
		default:
			return invalid("unknown input role")
		}
	}
	switch r.Operation {
	case OperationGenerate:
		if len(r.Inputs) > 0 {
			return invalid("generate does not accept input assets")
		}
	case OperationReference:
		if roles["reference"] < 1 || roles["source"] != 0 || roles["mask"] != 0 {
			return invalid("reference requires reference assets")
		}
	case OperationEdit:
		if roles["source"] != 1 || roles["mask"] != 0 {
			return invalid("edit requires one source and no mask")
		}
	case OperationMask:
		if roles["source"] != 1 || roles["mask"] != 1 {
			return invalid("mask requires exactly one source and one mask")
		}
	}
	_, err := NormalizeRequest(r)
	return err
}
func cloneRequest(r CreateJobRequest) CreateJobRequest {
	p := map[string]any{}
	for k, v := range r.Parameters {
		p[k] = cloneValue(v)
	}
	return CreateJobRequest{ProjectID: r.ProjectID, TargetID: r.TargetID, Operation: r.Operation, Prompt: r.Prompt, Inputs: append([]InputAsset{}, r.Inputs...), Parameters: p, Count: r.Count, ParentAssetID: r.ParentAssetID}
}
func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, n := range x {
			m[k] = cloneValue(n)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, n := range x {
			a[i] = cloneValue(n)
		}
		return a
	default:
		return v
	}
}
