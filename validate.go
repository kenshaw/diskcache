package diskcache

import (
	"net/http"
	"time"
)

// Validity indicates response validity.
type Validity int

// Validity states.
const (
	Error Validity = iota
	Retry
	Valid
)

// Validator is the shared interface for validating responses.
type Validator interface {
	// Validate validates the response based on
	Validate(*http.Request, *http.Response, time.Time, bool) (Validity, error)
}

// ValidatorFunc is a response validator func.
type ValidatorFunc func(*http.Request, *http.Response, time.Time, bool, int) (Validity, error)

// SimpleValidator is a simple response validator.
type SimpleValidator struct {
	count     int
	validator ValidatorFunc
}

// NewSimpleValidator creates a simple validator.
func NewSimpleValidator(validator ValidatorFunc) *SimpleValidator {
	v := &SimpleValidator{
		validator: validator,
	}
	return v
}

// Validate satisfies the Validator interface.
func (v *SimpleValidator) Validate(req *http.Request, res *http.Response, mod time.Time, stale bool) (Validity, error) {
	validity, err := v.validator(req, res, mod, stale, v.count)
	if err != nil {
		return Error, err
	}
	v.count++
	return validity, nil
}
