package permissions

import "fmt"

const (
	Normal     = "normal"
	Plan       = "plan"
	AutoEdit   = "auto_edit"
	FullAccess = "full_access"
	Always     = "always"
	AutoReview = "auto_review"
)

func Validate(mode string) error {
	switch mode {
	case "", Normal, Plan, AutoEdit, FullAccess, Always, AutoReview:
		return nil
	default:
		return fmt.Errorf("invalid permission mode %q", mode)
	}
}

func IsEdit(tool string) bool {
	switch tool {
	case "Write", "Edit", "write", "edit":
		return true
	default:
		return false
	}
}
