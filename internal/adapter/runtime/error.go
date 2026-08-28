package runtime

import "github.com/jack-wang-176/qemu-agent/internal/modeling"

type adapterError struct {
	Category string
	Cause    error
}

func (a *adapterError) Unwrap() error {
	return a.Cause
}

func (a *adapterError) Error() string {
	return "runtime adapter: " + a.Category
}

func (a *adapterError) ErrorCategory() string {
	if a == nil || a.Category == "" {
		return "internal"
	}
	return a.Category
}

func mapCurrentError(err error) error {
	if err == nil {
		return err
	}
	category := currentErrorCategory(err)
	return &adapterError{
		Category: category,
		Cause:    err,
	}
}

func currentErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	category := modeling.Category(err)
	if category == "" {
		category = "internal"
	}
	return category
}
