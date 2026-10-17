package backend

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const maxSessionLoops = 50
const loopLifetime = 7 * 24 * time.Hour

type loopSchedule struct {
	IntervalSeconds int64 `json:"intervalSeconds"`
	NextFireAt      int64 `json:"nextFireAt"`
	ExpiresAt       int64 `json:"expiresAt"`
	Iterations      int64 `json:"iterations"`
}

func parseLoopInterval(raw string) (int64, string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if len(raw) < 2 {
		return 0, "", errors.New("loop interval must be a positive number followed by s, m, h or d")
	}
	unit := map[byte]int64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}[raw[len(raw)-1]]
	digits := raw[:len(raw)-1]
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, "", errors.New("invalid loop interval")
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || unit == 0 || n > int64(loopLifetime/time.Second)/unit {
		return 0, "", errors.New("loop interval must be positive and no longer than seven days")
	}
	seconds := max(int64(60), n*unit)
	return seconds, compactLoopInterval(seconds), nil
}

func compactLoopInterval(seconds int64) string {
	for _, unit := range []struct {
		seconds int64
		suffix  string
	}{{86400, "d"}, {3600, "h"}, {60, "m"}, {1, "s"}} {
		if seconds%unit.seconds == 0 {
			return fmt.Sprintf("%d%s", seconds/unit.seconds, unit.suffix)
		}
	}
	return ""
}

func (c *cronStore) expireLoop(id string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	task := c.task(id)
	if task == nil || task.Loop == nil || !task.Enabled || now.UnixMilli() < task.Loop.ExpiresAt {
		return nil
	}
	old := cloneCronDisk(c.disk)
	task.Enabled = false
	c.disk.Revision++
	return c.commitLocked(old)
}

func (m *cronManager) tickLoop(task *CronTask, now time.Time) {
	if m.loopResults != nil {
		m.loopResults(task)
	}
	if !task.Enabled {
		return
	}
	if now.UnixMilli() >= task.Loop.ExpiresAt {
		m.report(m.store.expireLoop(task.ID, now))
		return
	}
	if now.UnixMilli() < task.Loop.NextFireAt {
		return
	}
	// Keep the bounded run log from evicting results while the parent stays busy.
	if runs, err := m.store.runs(task.ID, cronRunLimit); err == nil && len(runs) == cronRunLimit {
		for _, run := range runs {
			if !run.Delivered {
				return
			}
		}
	}
	// A slow occurrence is never overlapped or queued for catch-up.
	m.mu.Lock()
	busy := m.active[task.ID] != nil
	m.mu.Unlock()
	if busy {
		return
	}
	if _, err := m.start(task.ID, true); err != nil && err.Error() != "Cron task is already running." {
		m.report(err)
	}
}
