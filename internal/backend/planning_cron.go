package backend

import (
	"encoding/json"
	"errors"
	"sort"
	"time"
)

func (s *Server) planningCron(raw json.RawMessage) (any, error) {
	var q struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	}
	if e := json.Unmarshal(raw, &q); e != nil {
		return nil, e
	}
	if q.To <= q.From || q.To-q.From > 93*86400000 {
		return nil, errors.New("E:cron_range")
	}
	now := time.Now()
	tasks, occ, runs, summaries := []map[string]any{}, []map[string]any{}, []map[string]any{}, []map[string]any{}
	s.cron.store.mu.Lock()
	disk := cloneCronDisk(s.cron.store.disk)
	s.cron.store.mu.Unlock()
	for _, task := range disk.Tasks {
		if !task.Enabled || (task.RemainingExecutions != nil && *task.RemainingExecutions == 0) {
			continue
		}
		t := map[string]any{"id": task.ID, "name": task.Name, "cron": task.Cron, "kind": task.Type, "remainingExecutions": task.RemainingExecutions}
		type bucket struct {
			dates     []int64
			runs      []map[string]any
			truncated bool
		}
		days := map[string]*bucket{}
		get := func(at int64) *bucket {
			day := time.UnixMilli(at).In(time.Local).Format("2006-01-02")
			if days[day] == nil {
				days[day] = &bucket{}
			}
			return days[day]
		}
		for _, run := range disk.Runs {
			if run.TaskID != task.ID {
				continue
			}
			preview := []rune(run.Output)
			if len(preview) > 300 {
				preview = preview[:300]
			}
			r := map[string]any{"id": run.ID, "taskId": run.TaskID, "state": run.State, "success": run.Success, "startedAt": run.StartedAt, "finishedAt": run.FinishedAt, "durationMs": run.DurationMs, "exitCode": run.ExitCode, "outputPreview": string(preview)}
			if run.State == "done" || run.State == "expired" {
				if old, ok := t["lastRun"].(map[string]any); !ok || old["startedAt"].(int64) < run.StartedAt {
					t["lastRun"] = r
				}
			}
			if run.StartedAt >= q.From && run.StartedAt < q.To && run.StartedAt < now.UnixMilli() {
				b := get(run.StartedAt)
				b.runs = append(b.runs, r)
			}
		}
		sch, e := parseCron(task.Cron)
		if e != nil {
			return nil, e
		}
		start := time.UnixMilli(max(q.From, now.UnixMilli())).In(time.Local)
		end := time.UnixMilli(q.To)
		day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
		remaining := uint64(^uint64(0))
		if task.RemainingExecutions != nil {
			remaining = *task.RemainingExecutions
		}
		for ; day.Before(end) && remaining > 0; day = day.AddDate(0, 0, 1) {
			stop := false
			for hour := 0; hour < 24 && !stop; hour++ {
				if !sch.fields[2].values[hour] {
					continue
				}
				for minute := 0; minute < 60 && !stop; minute++ {
					if !sch.fields[1].values[minute] {
						continue
					}
					for second := 0; second < 60; second++ {
						if !sch.fields[0].values[second] {
							continue
						}
						at := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, second, 0, time.Local)
						if at.Before(start) || !at.Before(end) || !sch.matches(at) || at.Hour() != hour {
							continue
						}
						b := get(at.UnixMilli())
						if len(b.dates) >= 1440 {
							b.truncated = true
							stop = true
							break
						}
						b.dates = append(b.dates, at.UnixMilli())
						remaining--
						if remaining == 0 {
							stop = true
							break
						}
					}
				}
			}
		}
		for date, b := range days {
			if len(b.dates)+len(b.runs) > 8 || b.truncated {
				first, last, failed := int64(0), int64(0), 0
				for _, at := range b.dates {
					if first == 0 || at < first {
						first = at
					}
					last = max(last, at)
				}
				for _, r := range b.runs {
					at := r["startedAt"].(int64)
					if first == 0 || at < first {
						first = at
					}
					last = max(last, at)
					if r["state"] == "expired" || (r["state"] == "done" && r["success"] == false) {
						failed++
					}
				}
				summaries = append(summaries, map[string]any{"taskId": task.ID, "date": date, "planned": len(b.dates), "plannedTruncated": b.truncated, "ran": len(b.runs), "failed": failed, "firstAt": first, "lastAt": last})
			} else {
				for _, at := range b.dates {
					occ = append(occ, map[string]any{"taskId": task.ID, "at": at})
				}
				runs = append(runs, b.runs...)
			}
		}
		tasks = append(tasks, t)
	}
	sort.Slice(occ, func(i, j int) bool { return occ[i]["at"].(int64) < occ[j]["at"].(int64) })
	return map[string]any{"timeZone": time.Local.String(), "now": now.UnixMilli(), "tasks": tasks, "occurrences": occ, "runs": runs, "summaries": summaries}, nil
}
