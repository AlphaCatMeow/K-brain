package imaging

import (
	"encoding/json"
	"reflect"
	"time"
)

// Ownership lives in durable envelopes, never in public request/response objects.
type recordScope struct {
	OwnerID   string `json:"ownerId"`
	BackendID string `json:"backendId"`
}

func owner(s Scope) recordScope { return recordScope{s.OwnerID, s.BackendID} }
func (s recordScope) matches(v Scope) bool {
	return s == owner(v) && v.OwnerID != "" && v.BackendID != ""
}

type projectRecord struct {
	Scope     recordScope  `json:"scope"`
	Value     ImageProject `json:"value"`
	DeletedAt *time.Time   `json:"deletedAt,omitempty"`
}
type jobRecord struct {
	Scope             recordScope `json:"scope"`
	Value             ImageJob    `json:"value"`
	DeletedAt         *time.Time  `json:"deletedAt,omitempty"`
	LastEventSeq      uint64      `json:"lastEventSeq"`
	FirstAvailableSeq uint64      `json:"firstAvailableSeq"`
}
type assetRecord struct {
	Scope recordScope `json:"scope"`
	Value ImageAsset  `json:"value"`
}
type IdempotencyRecord struct {
	Hash      string
	JobID     string
	ExpiresAt time.Time
}
type idempotencyEntry struct {
	Scope recordScope       `json:"scope"`
	Key   string            `json:"key"`
	Value IdempotencyRecord `json:"value"`
}

// ExecutionRecord is an internal API; marshaling it as a response is prohibited.
// Durable storage uses the separate typed executionDisk projection below.
type ExecutionRecord struct {
	JobID    string
	Items    map[string]ItemExecution
	Deadline time.Time
}
type ItemExecution struct {
	ProviderHandle   string
	SubmissionKey    string
	Continuation     []byte
	Attempts         []ImageAttempt
	LeaseOwner       string
	LeaseExpiresAt   time.Time
	NextPollAt       time.Time
	QueryFailures    int
	DownloadAttempts int
}

func (ExecutionRecord) MarshalJSON() ([]byte, error) {
	return nil, invalid("execution record is private")
}
func (ItemExecution) MarshalJSON() ([]byte, error) { return nil, invalid("execution item is private") }

type attemptDisk struct {
	AttemptNo       int         `json:"attemptNo"`
	StartedAt       time.Time   `json:"startedAt"`
	SubmissionState string      `json:"submissionState"`
	RequestID       string      `json:"requestId"`
	ProviderHandle  string      `json:"providerHandle"`
	Delivery        Delivery    `json:"delivery"`
	Error           *ImageError `json:"error,omitempty"`
	Usage           *Usage      `json:"usage"`
}
type itemExecutionDisk struct {
	ProviderHandle   string        `json:"providerHandle"`
	SubmissionKey    string        `json:"submissionKey"`
	Continuation     []byte        `json:"continuation"`
	Attempts         []attemptDisk `json:"attempts"`
	LeaseOwner       string        `json:"leaseOwner"`
	LeaseExpiresAt   time.Time     `json:"leaseExpiresAt"`
	NextPollAt       time.Time     `json:"nextPollAt"`
	QueryFailures    int           `json:"queryFailures"`
	DownloadAttempts int           `json:"downloadAttempts"`
}
type executionDisk struct {
	Scope    recordScope                  `json:"scope"`
	JobID    string                       `json:"jobId"`
	Items    map[string]itemExecutionDisk `json:"items"`
	Deadline time.Time                    `json:"deadline"`
}

func executionToDisk(scope Scope, v ExecutionRecord) executionDisk {
	d := executionDisk{Scope: owner(scope), JobID: v.JobID, Deadline: v.Deadline, Items: map[string]itemExecutionDisk{}}
	for id, i := range v.Items {
		x := itemExecutionDisk{ProviderHandle: i.ProviderHandle, SubmissionKey: i.SubmissionKey, Continuation: cloneStoreValue(i.Continuation), LeaseOwner: i.LeaseOwner, LeaseExpiresAt: i.LeaseExpiresAt, NextPollAt: i.NextPollAt, QueryFailures: i.QueryFailures, DownloadAttempts: i.DownloadAttempts}
		for _, a := range i.Attempts {
			x.Attempts = append(x.Attempts, attemptDisk{a.AttemptNo, a.StartedAt, a.SubmissionState, a.RequestID, a.ProviderHandle, a.Delivery, cloneStoreValue(a.Error), cloneStoreValue(a.Usage)})
		}
		d.Items[id] = x
	}
	return d
}
func executionFromDisk(d executionDisk) ExecutionRecord {
	v := ExecutionRecord{JobID: d.JobID, Deadline: d.Deadline, Items: map[string]ItemExecution{}}
	for id, x := range d.Items {
		i := ItemExecution{ProviderHandle: x.ProviderHandle, SubmissionKey: x.SubmissionKey, Continuation: cloneStoreValue(x.Continuation), LeaseOwner: x.LeaseOwner, LeaseExpiresAt: x.LeaseExpiresAt, NextPollAt: x.NextPollAt, QueryFailures: x.QueryFailures, DownloadAttempts: x.DownloadAttempts}
		for _, a := range x.Attempts {
			i.Attempts = append(i.Attempts, ImageAttempt{AttemptNo: a.AttemptNo, StartedAt: a.StartedAt, SubmissionState: a.SubmissionState, RequestID: a.RequestID, ProviderHandle: a.ProviderHandle, Delivery: a.Delivery, Error: cloneStoreValue(a.Error), Usage: cloneStoreValue(a.Usage)})
		}
		v.Items[id] = i
	}
	return v
}

type storeData struct {
	Projects    map[string]projectRecord    `json:"projects"`
	Jobs        map[string]jobRecord        `json:"jobs"`
	Assets      map[string]assetRecord      `json:"assets"`
	Events      map[string][]ImageEvent     `json:"events"`
	Idempotency map[string]idempotencyEntry `json:"idempotency"`
	Executions  map[string]executionDisk    `json:"executions"`
}

func newStoreData() storeData {
	return storeData{map[string]projectRecord{}, map[string]jobRecord{}, map[string]assetRecord{}, map[string][]ImageEvent{}, map[string]idempotencyEntry{}, map[string]executionDisk{}}
}
func idemIndex(s Scope, key string) string {
	b, _ := json.Marshal([]string{s.OwnerID, s.BackendID, key})
	return string(b)
}

// Clone the entire transaction graph, including map[string]any and optional pointers.
// Reflection preserves numeric types and does not call public JSON projections.
func cloneStoreValue[T any](v T) T {
	return cloneStoreReflect(reflect.ValueOf(&v).Elem()).Interface().(T)
}
func cloneStoreReflect(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		r := reflect.New(v.Type()).Elem()
		r.Set(cloneStoreReflect(v.Elem()))
		return r
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		r := reflect.New(v.Type().Elem())
		r.Elem().Set(cloneStoreReflect(v.Elem()))
		return r
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		r := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			r.SetMapIndex(it.Key(), cloneStoreReflect(it.Value()))
		}
		return r
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		r := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			r.Index(i).Set(cloneStoreReflect(v.Index(i)))
		}
		return r
	case reflect.Struct:
		r := reflect.New(v.Type()).Elem()
		r.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if r.Field(i).CanSet() && v.Type().Field(i).IsExported() {
				r.Field(i).Set(cloneStoreReflect(v.Field(i)))
			}
		}
		return r
	case reflect.Array:
		r := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			r.Index(i).Set(cloneStoreReflect(v.Index(i)))
		}
		return r
	default:
		return v
	}
}
