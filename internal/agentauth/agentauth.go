// Package agentauth authenticates agent bearer tokens for the JSON API and
// the MCP endpoint. The workspace is resolved from the token only.
package agentauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

var ErrUnauthorized = errors.New("unauthorized")

type Authenticator struct {
	Store          *store.Store
	Limiter        *auth.Limiter
	TrustedProxies []netip.Prefix
}

type ctxKey struct{}

func (a *Authenticator) Verify(ctx context.Context, token, ip string) (service.Principal, error) {
	if a.Limiter.Blocked(ip) {
		return service.Principal{}, ErrUnauthorized
	}
	if !strings.HasPrefix(token, "dgw_") || len(token) != 47 {
		a.Limiter.Fail(ip)
		return service.Principal{}, ErrUnauthorized
	}
	t, ws, err := a.Store.TokenByHash(ctx, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		a.Limiter.Fail(ip)
		return service.Principal{}, ErrUnauthorized
	}
	if err != nil {
		return service.Principal{}, err
	}
	if err := a.Store.TouchToken(ctx, t.ID); err != nil {
		return service.Principal{}, err
	}
	return service.Principal{Workspace: ws, TokenID: t.ID, ClientIP: ip}, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := auth.ClientIP(r, a.TrustedProxies)
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		p, err := a.Verify(r.Context(), token, ip)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			if errors.Is(err, ErrUnauthorized) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintln(w, `{"code":"unauthorized","message":"unauthorized"}`)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, `{"code":"internal","message":"internal error"}`)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func FromContext(ctx context.Context) (service.Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(service.Principal)
	return p, ok
}

// MCPVerifier adapts Verify to the MCP SDK's bearer-token middleware. UserID
// binds MCP sessions to the token that created them.
func (a *Authenticator) MCPVerifier() sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, r *http.Request) (*sdkauth.TokenInfo, error) {
		p, err := a.Verify(ctx, token, auth.ClientIP(r, a.TrustedProxies))
		if errors.Is(err, ErrUnauthorized) {
			return nil, sdkauth.ErrInvalidToken
		}
		if err != nil {
			return nil, err
		}
		return &sdkauth.TokenInfo{UserID: fmt.Sprintf("token:%d", p.TokenID), Extra: map[string]any{"principal": p}}, nil
	}
}
