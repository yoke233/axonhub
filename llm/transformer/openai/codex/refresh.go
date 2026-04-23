package codex

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/oauth"
)

const (
	// RequestRefreshBefore proactively refreshes OAuth credentials shortly before
	// expiry so Codex requests do not race an about-to-expire bearer token.
	RequestRefreshBefore = 30 * time.Minute

	// AutoRefreshBefore keeps the background Codex refresh loop ahead of expiry
	// without needing a separate auth-pool scheduler.
	AutoRefreshBefore = 30 * time.Minute
)

type ensureFreshTokenGetter interface {
	EnsureFresh(ctx context.Context, refreshBefore time.Duration) (*oauth.OAuthCredentials, error)
}

type forceRefreshTokenGetter interface {
	RefreshNow(ctx context.Context) (*oauth.OAuthCredentials, error)
}

func getRequestCredentials(ctx context.Context, getter oauth.TokenGetter) (*oauth.OAuthCredentials, error) {
	if refresher, ok := getter.(ensureFreshTokenGetter); ok {
		return refresher.EnsureFresh(ctx, RequestRefreshBefore)
	}

	return getter.Get(ctx)
}

func supportsUnauthorizedRetry(getter oauth.TokenGetter, err error) bool {
	if getter == nil {
		return false
	}

	if _, ok := getter.(forceRefreshTokenGetter); !ok {
		return false
	}

	var httpErr *httpclient.Error
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

func refreshUnauthorizedCredentials(ctx context.Context, getter oauth.TokenGetter) error {
	refresher, ok := getter.(forceRefreshTokenGetter)
	if !ok {
		return errors.New("token provider does not support forced refresh")
	}

	_, err := refresher.RefreshNow(ctx)
	return err
}
