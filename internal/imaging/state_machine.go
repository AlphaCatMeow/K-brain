package imaging

import (
	"math/big"
	"strings"
)

func terminalJob(s JobStatus) bool {
	switch s {
	case JobSucceeded, JobPartialSucceeded, JobFailed, JobCancelled, JobResultUnknown:
		return true
	}
	return false
}

// IsTerminal is shared by transport stream termination and shutdown callers.
func IsTerminal(s JobStatus) bool { return terminalJob(s) }
func aggregateJob(j *ImageJob, ex ExecutionRecord) {
	if terminalJob(j.Status) {
		return
	}
	active, good, bad, unknown, cancelled := false, 0, 0, false, false
	var first *ImageError
	for _, i := range j.Items {
		if !terminalJob(i.Status) {
			active = true
		}
		switch i.Status {
		case JobSucceeded:
			good++
		case JobPartialSucceeded:
			good++
			bad++
		case JobFailed:
			bad++
		case JobResultUnknown:
			unknown = true
		case JobCancelled:
			cancelled = true
		}
		if first == nil && i.Error != nil {
			first = i.Error
		}
	}
	j.Usage = aggregateUsage(j.Items, ex)
	if active {
		if j.Status != JobCancelRequested {
			j.Status = JobRunning
		}
		return
	}
	switch {
	case unknown:
		j.Status = JobResultUnknown
	case good > 0 && (bad > 0 || cancelled):
		j.Status = JobPartialSucceeded
	case good > 0:
		j.Status = JobSucceeded
	case cancelled:
		j.Status = JobCancelled
	default:
		j.Status = JobFailed
	}
	j.Error = first
}
func aggregateUsage(items []ImageItem, ex ExecutionRecord) *Usage {
	sum := new(big.Rat)
	var result *Usage
	precision := 0
	for _, i := range items {
		x := ex.Items[i.ID]
		if len(x.Attempts) == 0 {
			return nil
		}
		u := x.Attempts[len(x.Attempts)-1].Usage
		if u == nil || u.Amount == nil {
			return nil
		}
		decimal, err := decimalNumber(*u.Amount)
		if err != nil {
			return nil
		}
		n, ok := new(big.Rat).SetString(decimal)
		if !ok {
			return nil
		}
		if result == nil {
			result = cloneUsage(u)
		} else if result.Currency != u.Currency || result.Source != u.Source {
			return nil
		}
		result.Estimated = result.Estimated || u.Estimated
		sum.Add(sum, n)
		if dot := strings.IndexByte(decimal, '.'); dot >= 0 && len(decimal)-dot-1 > precision {
			precision = len(decimal) - dot - 1
		}
	}
	if result == nil {
		return nil
	}
	v := sum.FloatString(precision)
	result.Amount = &v
	return result
}
