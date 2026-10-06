package planning

import (
	"encoding/hex"
	"math"
	"strings"
)

// JSON numbers must remain exact integers across browser and native clients.
func validateFields(item Item) error {
	for key, value := range item {
		if value == nil {
			continue
		}
		switch {
		case strings.Contains(" revision sortOrder createdAt updatedAt deletedAt completedAt dueAt startAt endAt triggerAt snoozedUntil notifiedAt leaseUntil nextAttemptAt attempts reminderMinutes estimateMinutes interval count minutes exceptionRevision relativeRevision ", " "+key+" "):
			n, ok := value.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || math.Abs(n) > 9007199254740991 {
				return errCode("invalid_integer:" + key)
			}
		case strings.Contains(" isDefault readOnly dueReminder delete deleteTasks ", " "+key+" "):
			if _, ok := value.(bool); !ok {
				return errCode("invalid_boolean:" + key)
			}
		case strings.Contains(" time recurrence schedule ", " "+key+" "):
			child, ok := value.(map[string]any)
			if !ok {
				return errCode("invalid_object:" + key)
			}
			if e := validateFields(child); e != nil {
				return e
			}
		case key == "tagIds" || key == "excludedDates" || key == "weekdays":
			values, ok := value.([]any)
			if !ok {
				return errCode("invalid_array:" + key)
			}
			for _, entry := range values {
				if key == "weekdays" {
					n, ok := entry.(float64)
					if !ok || n < 0 || n > 6 || math.Trunc(n) != n {
						return errCode("recurrence_invalid")
					}
				} else if _, ok := entry.(string); !ok {
					return errCode("invalid_array:" + key)
				}
			}
		case strings.Contains(" id title titleOverride name notes color status priority calendarId todoId seriesId originalDate groupId parentId dueDate dueTimeZone timeZone kind startDate endDateExclusive frequency until targetType targetId origin providerKind accountId externalId ", " "+key+" "):
			if _, ok := value.(string); !ok {
				return errCode("invalid_string:" + key)
			}
		}
	}
	return nil
}

func validColor(color string) bool {
	if len(color) != 7 || color[0] != '#' {
		return false
	}
	_, err := hex.DecodeString(color[1:])
	return err == nil
}

func normalize(v *Snapshot) {
	for _, items := range []*[]Item{&v.Calendars, &v.Todos, &v.Groups, &v.Tags, &v.Events, &v.TodoSchedules, &v.Reminders, &v.Sources, &v.Subscriptions} {
		if *items == nil {
			*items = []Item{}
		}
	}
}
