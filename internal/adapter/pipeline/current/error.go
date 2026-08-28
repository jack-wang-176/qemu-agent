package current

import "github.com/jack-wang-176/qemu-agent/internal/modeling"

type currentError struct {
	cause    error
	category string
}

func (c *currentError) Unwrap() error {
	return c.cause
}

func (c *currentError) Error() string {
	return "current engine: " + c.category
}

func (c *currentError) ErrorCategory() string {
	if c == nil || c.category == "" {
		return "internal"
	}
	return c.category
}

func mapCurrentError(err error) error {
	if err == nil {
		return err
	}
	category := currentErrorCategory(err)
	return &currentError{
		cause:    err,
		category: category,
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
