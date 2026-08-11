package domain

import "errors"

var (
	ErrNotFound            = errors.New("resource not found")
	ErrValidation          = errors.New("validation failed")
	ErrIdempotencyConflict = errors.New("idempotency key was used with a different request")
	ErrStateConflict       = errors.New("operation is not valid in the current state")
	ErrVersionConflict     = errors.New("resource version is stale")
	ErrInvariantViolation  = errors.New("financial invariant violated")
	ErrDependency          = errors.New("required dependency is unavailable")
)
