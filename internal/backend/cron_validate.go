package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var cronKinds = map[string]bool{"bash": true, "http": true, "prompt": true}
var cronReasoning = map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}

func validateCronTasks(tasks []*CronTask) error {
	seen := map[string]bool{}
	for _, task := range tasks {
		if task == nil {
			return errors.New("cron task cannot be null")
		}
		if err := validateCronTask(task); err != nil {
			return err
		}
		if seen[task.ID] {
			return fmt.Errorf("duplicate cron task id %q", task.ID)
		}
		seen[task.ID] = true
	}
	return nil
}
func validateCronTask(task *CronTask) error {
	task.ID = strings.TrimSpace(task.ID)
	task.Name = strings.TrimSpace(task.Name)
	task.Description = strings.TrimSpace(task.Description)
	task.Cron = strings.TrimSpace(task.Cron)
	task.Workdir = strings.TrimSpace(task.Workdir)
	task.Reasoning = strings.TrimSpace(task.Reasoning)
	if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Name) == "" {
		return errors.New("cron task id and name are required")
	}
	if err := validateCronExpression(task.Cron); err != nil {
		return err
	}
	if !cronKinds[task.Type] {
		return fmt.Errorf("unsupported cron task type %q", task.Type)
	}

	maxTimeout := uint64(600)
	if task.Type == "prompt" {
		maxTimeout = 3600
	}
	if task.TimeoutSeconds < 1 || task.TimeoutSeconds > maxTimeout {
		return fmt.Errorf("cron timeout must be between 1 and %d seconds", maxTimeout)
	}
	if task.RemainingExecutions != nil && *task.RemainingExecutions == 0 {
		task.Enabled = false
	}
	if task.Type == "bash" && strings.TrimSpace(task.Script) == "" {
		return errors.New("bash cron task script is required")
	}
	if task.Type == "prompt" {
		if strings.TrimSpace(task.Prompt) == "" {
			return errors.New("prompt cron task prompt is required")
		}
		if task.SelectedModel == nil || strings.TrimSpace(task.SelectedModel.CustomProviderID) == "" || strings.TrimSpace(task.SelectedModel.Model) == "" {
			return errors.New("prompt cron task selectedModel is required")
		}
		if task.Reasoning != "" && !cronReasoning[task.Reasoning] {
			return fmt.Errorf("unsupported prompt reasoning %q", task.Reasoning)
		}
	}
	if task.Type == "http" {
		if len(task.Requests) == 0 {
			return errors.New("http cron task requires a request")
		}
		for i := range task.Requests {
			if err := validateCronRequest(&task.Requests[i]); err != nil {
				return fmt.Errorf("cron request %d: %w", i, err)
			}
		}
	}
	if task.Type != "http" && strings.TrimSpace(task.Workdir) != "" {
		if !filepath.IsAbs(task.Workdir) {
			return errors.New("cron workdir must be an absolute path")
		}
	}
	if task.Type == "http" {
		task.Workdir = ""
	}
	return nil
}
func validateCronRequest(req *CronHTTPRequest) error {
	if strings.TrimSpace(req.ID) == "" {
		req.ID = newRunID()
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("url must be an absolute HTTP(S) URL without credentials")
	}
	req.URL = u.String()
	req.Method = strings.ToUpper(strings.TrimSpace(req.Method))
	if req.Method == "" {
		req.Method = "POST"
	}
	switch req.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
	default:
		return fmt.Errorf("unsupported HTTP method %q", req.Method)
	}
	if req.Body != nil && len(req.Body) > 1<<20 {
		return errors.New("request body is too large")
	}
	for k, v := range req.Headers {
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
			return errors.New("invalid request header")
		}
	}
	return nil
}
func validateCronExpression(expression string) error {
	if _, err := parseCron(expression); err != nil {
		return err
	}
	return nil
}

type cronField struct {
	min, max int
	values   map[int]bool
}
type cronSchedule struct {
	fields                           [6]cronField
	dayRestricted, weekdayRestricted bool
}

func parseCron(expression string) (*cronSchedule, error) {
	parts := strings.Fields(strings.TrimSpace(expression))
	if len(parts) != 6 {
		return nil, errors.New("cron expression must have six fields (second minute hour day month weekday)")
	}
	limits := [6][2]int{{0, 59}, {0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	s := &cronSchedule{dayRestricted: parts[3] != "*" && parts[3] != "?", weekdayRestricted: parts[5] != "*" && parts[5] != "?"}
	for i, part := range parts {
		part = strings.ToUpper(part)
		if i == 4 {
			for month, name := range []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"} {
				part = strings.ReplaceAll(part, name, strconv.Itoa(month+1))
			}
		}
		if i == 5 {
			for day, name := range []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"} {
				part = strings.ReplaceAll(part, name, strconv.Itoa(day))
			}
		}
		if (i == 3 || i == 5) && part == "?" {
			part = "*"
		}
		field, err := parseCronField(part, limits[i][0], limits[i][1])
		if err != nil {
			return nil, fmt.Errorf("invalid cron field %d: %w", i+1, err)
		}
		if i == 5 && field.values[7] {
			field.values[0] = true
		}
		s.fields[i] = field
	}
	return s, nil
}
func parseCronField(raw string, minValue, maxValue int) (cronField, error) {
	field := cronField{min: minValue, max: maxValue, values: map[int]bool{}}
	for _, item := range strings.Split(raw, ",") {
		if item == "" {
			return field, errors.New("empty list item")
		}
		base, step := item, 1
		if strings.Contains(item, "/") {
			var ok bool
			base, _, ok = strings.Cut(item, "/")
			if !ok {
				return field, errors.New("invalid step")
			}
			var err error
			step, err = strconv.Atoi(item[strings.Index(item, "/")+1:])
			if err != nil || step < 1 {
				return field, errors.New("step must be positive")
			}
		}
		lo, hi := minValue, maxValue
		if base != "*" {
			if strings.Contains(base, "-") {
				pieces := strings.Split(base, "-")
				if len(pieces) != 2 {
					return field, errors.New("invalid range")
				}
				var err error
				if lo, err = strconv.Atoi(pieces[0]); err != nil {
					return field, errors.New("invalid range")
				}
				if hi, err = strconv.Atoi(pieces[1]); err != nil {
					return field, errors.New("invalid range")
				}
			} else {
				var err error
				if lo, err = strconv.Atoi(base); err != nil {
					return field, errors.New("invalid value")
				}
				hi = lo
				if strings.Contains(item, "/") {
					hi = maxValue
				}
			}
		}
		if lo < minValue || hi > maxValue || lo > hi {
			return field, errors.New("value out of range")
		}
		for value := lo; value <= hi; {
			field.values[value] = true
			if step > hi-value {
				break
			}
			value += step
		}
	}
	return field, nil
}
func (s *cronSchedule) matches(t time.Time) bool {
	values := [6]int{t.Second(), t.Minute(), t.Hour(), t.Day(), int(t.Month()), int(t.Weekday())}
	for _, i := range []int{0, 1, 2, 4} {
		if !s.fields[i].values[values[i]] {
			return false
		}
	}
	day := s.fields[3].values[values[3]]
	weekday := s.fields[5].values[values[5]]
	if s.dayRestricted && s.weekdayRestricted {
		return day || weekday
	}
	return day && weekday
}
func cronBody(req CronHTTPRequest) ([]byte, error) {
	if len(req.Body) == 0 || string(req.Body) == "null" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(req.Body, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
