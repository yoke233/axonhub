package httpclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type contextDiagnostics struct {
	state       string
	err         error
	hasDeadline bool
	deadline    time.Time
	remaining   time.Duration
}

func inspectContext(ctx context.Context, now time.Time) contextDiagnostics {
	if ctx == nil {
		return contextDiagnostics{state: "nil"}
	}

	diag := contextDiagnostics{err: ctx.Err()}
	if deadline, ok := ctx.Deadline(); ok {
		diag.hasDeadline = true
		diag.deadline = deadline
		diag.remaining = deadline.Sub(now)
	}

	switch {
	case diag.err == nil:
		diag.state = "active"
	case errors.Is(diag.err, context.DeadlineExceeded):
		diag.state = "deadline_exceeded"
	case errors.Is(diag.err, context.Canceled):
		diag.state = "canceled"
	default:
		diag.state = "done"
	}

	return diag
}

func streamRequestFailureDetail(ctx context.Context) string {
	diag := inspectContext(ctx, time.Now())
	switch {
	case diag.err == nil:
		return "request context is still active; failure is from outbound transport, upstream connection, or provider response path"
	case errors.Is(diag.err, context.DeadlineExceeded):
		return contextDetailMessage("AxonHub request deadline exceeded", "axonhub_request_deadline", diag)
	case errors.Is(diag.err, context.Canceled):
		owner := "downstream_client_or_proxy_disconnected_or_server_shutdown"
		if diag.hasDeadline && diag.remaining <= 0 {
			owner = "context_canceled_at_or_after_deadline"
		}

		return contextDetailMessage("request context canceled before upstream stream completed", owner, diag)
	default:
		return contextDetailMessage("request context finished before upstream stream completed", "unknown_context_owner", diag)
	}
}

func contextDetailMessage(prefix string, owner string, diag contextDiagnostics) string {
	msg := fmt.Sprintf("%s (owner_inference=%s, context_state=%s", prefix, owner, diag.state)
	if diag.err != nil {
		msg += fmt.Sprintf(", context_error=%v", diag.err)
	}
	if diag.hasDeadline {
		msg += fmt.Sprintf(
			", context_deadline=%s, context_remaining_ms=%d",
			diag.deadline.Format(time.RFC3339Nano),
			diag.remaining.Milliseconds(),
		)
	}

	return msg + ")"
}

func streamRequestFailureAttrs(ctx context.Context) []any {
	diag := inspectContext(ctx, time.Now())
	attrs := []any{
		slog.String("request_context_state", diag.state),
		slog.String("failure_detail", streamRequestFailureDetail(ctx)),
	}
	if diag.err != nil {
		attrs = append(attrs, slog.Any("request_context_error", diag.err))
	}
	if diag.hasDeadline {
		attrs = append(attrs,
			slog.Time("request_context_deadline", diag.deadline),
			slog.Int64("request_context_remaining_ms", diag.remaining.Milliseconds()),
		)
	}

	return attrs
}
