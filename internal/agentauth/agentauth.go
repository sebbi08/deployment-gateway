// Package agentauth authenticates agent bearer tokens for the JSON API and
// the MCP endpoint. The workspace is resolved from the token only.
package agentauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

var ErrUnauthorized = errors.New("unauthorized")

type Authenticator struct {
	Store          *store.Store
	Limiter        *auth.Limiter
	TrustedProxies []netip.Prefix
	Logger         *slog.Logger // nil: slog.Default()
	// MaxInFlight caps concurrent calls per token (0: unlimited).
	MaxInFlight int
	// RateBurst and RatePerSecond define a token bucket per token
	// (RateBurst 0: unlimited).
	RateBurst     int
	RatePerSecond float64
	Now           func() time.Time // nil: time.Now

	mu       sync.Mutex
	inFlight map[int64]int
	buckets  map[int64]*bucket
}

type ctxKey struct{}

// Verify resolves a token to its workspace. A valid token is accepted even
// from a blocked IP: the IP is often a shared proxy address, and 256-bit
// tokens make blocking irrelevant against guessing. The limiter only keeps
// failing clients from flooding the log.
func (a *Authenticator) Verify(ctx context.Context, token, ip string) (service.Principal, error) {
	if !strings.HasPrefix(token, "dgw_") || len(token) != 47 {
		return service.Principal{}, a.fail(ip, "malformed token")
	}
	t, ws, err := a.Store.TokenByHash(ctx, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return service.Principal{}, a.fail(ip, "unknown or revoked token")
	}
	if err != nil {
		return service.Principal{}, err
	}
	if err := a.Store.TouchToken(ctx, t.ID); err != nil {
		return service.Principal{}, err
	}
	return service.Principal{Workspace: ws, TokenID: t.ID, ClientIP: ip}, nil
}

// fail counts a failed attempt and logs it, unless the IP is already blocked.
func (a *Authenticator) fail(ip, reason string) error {
	if a.Limiter.Blocked(ip) {
		return ErrUnauthorized
	}
	log := a.Logger
	if log == nil {
		log = slog.Default()
	}
	if a.Limiter.Fail(ip) {
		log.Warn("agent auth blocked: too many failures; further failures from this address are not logged",
			"client_ip", ip, "reason", reason)
	} else {
		log.Warn("agent auth failed", "client_ip", ip, "reason", reason)
	}
	return ErrUnauthorized
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
		if msg, ok := a.acquire(p.TokenID); !ok {
			writeTooMany(w, msg)
			return
		}
		defer a.release(p.TokenID)
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

// InFlight reports the number of calls currently holding a slot.
func (a *Authenticator) InFlight() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, c := range a.inFlight {
		n += c
	}
	return n
}

type bucket struct {
	tokens float64
	last   time.Time
}

// acquire admits a call of the token: it must have a rate-limit token left
// and a free MaxInFlight slot. It returns an error message when it does not.
func (a *Authenticator) acquire(tokenID int64) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.RateBurst > 0 {
		now := time.Now()
		if a.Now != nil {
			now = a.Now()
		}
		if a.buckets == nil {
			a.buckets = map[int64]*bucket{}
		}
		b, ok := a.buckets[tokenID]
		if !ok {
			b = &bucket{tokens: float64(a.RateBurst), last: now}
			a.buckets[tokenID] = b
		}
		b.tokens = min(float64(a.RateBurst), b.tokens+now.Sub(b.last).Seconds()*a.RatePerSecond)
		b.last = now
		if b.tokens < 1 {
			return "too many calls for this token; slow down", false
		}
		b.tokens--
	}
	if a.MaxInFlight > 0 {
		if a.inFlight == nil {
			a.inFlight = map[int64]int{}
		}
		if a.inFlight[tokenID] >= a.MaxInFlight {
			return "too many concurrent calls for this token; retry later", false
		}
		a.inFlight[tokenID]++
	}
	return "", true
}

func (a *Authenticator) release(tokenID int64) {
	if a.MaxInFlight <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inFlight[tokenID]--; a.inFlight[tokenID] <= 0 {
		delete(a.inFlight, tokenID)
	}
}

func writeTooMany(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	b, _ := json.Marshal(map[string]string{"code": "too_many_requests", "message": msg})
	w.Write(append(b, '\n'))
}

// LimitMCP caps concurrent MCP requests per token. It must run behind the
// SDK's bearer-token middleware, which puts the verified token in the context.
func (a *Authenticator) LimitMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := sdkauth.TokenInfoFromContext(r.Context())
		p, ok := service.Principal{}, false
		if info != nil {
			p, ok = info.Extra["principal"].(service.Principal)
		}
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if msg, ok := a.acquire(p.TokenID); !ok {
			writeTooMany(w, msg)
			return
		}
		defer a.release(p.TokenID)
		next.ServeHTTP(w, r)
	})
}

// MaxBodyBytes bounds an agent request: the stack limit plus room for the
// request envelope (spec §7).
const MaxBodyBytes = stack.MaxRequestBytes + 4<<10

// bodyReadTimeout bounds how long a client may take to send its body.
const bodyReadTimeout = 30 * time.Second

// ReadBodyFirst buffers the whole (bounded) request body before calling next,
// so the token is checked only once the request is complete: a client that
// holds back its body cannot use a token after it was revoked.
func ReadBodyFirst(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(bodyReadTimeout)) // unsupported by test recorders
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
		_ = rc.SetReadDeadline(time.Time{})
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				fmt.Fprintf(w, `{"code":"validation_failed","message":"request body exceeds %d bytes"}`+"\n", MaxBodyBytes)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, `{"code":"validation_failed","message":"cannot read request body"}`)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
