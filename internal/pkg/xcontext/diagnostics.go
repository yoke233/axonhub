package xcontext

import (
	"context"
	"errors"
	"time"
)

const (
	ContextStateNil              = "nil"
	ContextStateActive           = "active"
	ContextStateCanceled         = "canceled"
	ContextStateDeadlineExceeded = "deadline_exceeded"
	ContextStateDone             = "done"
)

type Diagnostics struct {
	State       string
	Err         error
	HasDeadline bool
	Deadline    time.Time
	Remaining   time.Duration
}

func Inspect(ctx context.Context, now time.Time) Diagnostics {
	if ctx == nil {
		return Diagnostics{State: ContextStateNil}
	}

	diag := Diagnostics{Err: ctx.Err()}
	if deadline, ok := ctx.Deadline(); ok {
		diag.HasDeadline = true
		diag.Deadline = deadline
		diag.Remaining = time.Until(deadline)
		if !now.IsZero() {
			diag.Remaining = deadline.Sub(now)
		}
	}

	switch {
	case diag.Err == nil:
		diag.State = ContextStateActive
	case errors.Is(diag.Err, context.DeadlineExceeded):
		diag.State = ContextStateDeadlineExceeded
	case errors.Is(diag.Err, context.Canceled):
		diag.State = ContextStateCanceled
	default:
		diag.State = ContextStateDone
	}

	return diag
}
