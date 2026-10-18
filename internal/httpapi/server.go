package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"example.com/wallet/internal/domain"
	"example.com/wallet/internal/messaging"
	"example.com/wallet/internal/store"
	"example.com/wallet/internal/telemetry"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type Auth struct{ Verifier *oidc.IDTokenVerifier }
type identity struct {
	Provider string
	Internal bool
}
type identityKey struct{}
type Server struct {
	Publisher                          *messaging.Publisher
	Consumer                           *messaging.Consumer
	Store                              *store.Store
	Auth                               *Auth
	Requests                           atomic.Int64
	Conflicts, Replays, DurationMicros atomic.Int64
	Divergences                        atomic.Int64
}

func New(s *store.Store, a *Auth, c *messaging.Consumer, p *messaging.Publisher, _ *messaging.PendingWorker) *Server {
	return &Server{Store: s, Auth: a, Consumer: c, Publisher: p}
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code string) {
	reply(w, status, map[string]string{"code": code})
}
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		problem(w, 400, "INVALID_INPUT")
	case errors.Is(err, store.ErrNotFound):
		problem(w, 404, "NOT_FOUND")
	case errors.Is(err, store.ErrConflict):
		problem(w, 409, "CONFLICT")
	default:
		slog.Error("request failed", "error", err)
		problem(w, 503, "TEMPORARILY_UNAVAILABLE")
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return domain.ErrInvalid
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return domain.ErrInvalid
	}
	return nil
}
func uuidString(v string) (string, error) {
	u, err := uuid.Parse(v)
	if err != nil || u == uuid.Nil {
		return "", domain.ErrInvalid
	}
	return u.String(), nil
}
func (s *Server) secure(internal bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		if !strings.HasPrefix(raw, "Bearer ") {
			problem(w, 401, "UNAUTHORIZED")
			return
		}
		token, err := s.Auth.Verifier.Verify(r.Context(), strings.TrimPrefix(raw, "Bearer "))
		if err != nil {
			problem(w, 401, "UNAUTHORIZED")
			return
		}
		var claims struct {
			Azp   string `json:"azp"`
			Realm struct {
				Roles []string `json:"roles"`
			} `json:"realm_access"`
		}
		if err = token.Claims(&claims); err != nil || claims.Azp == "" {
			problem(w, 401, "UNAUTHORIZED")
			return
		}
		id := identity{Provider: claims.Azp}
		provider := false
		for _, role := range claims.Realm.Roles {
			if role == "wallet-internal" {
				id.Internal = true
			}
			if role == "provider" {
				provider = true
			}
		}
		if (internal && !id.Internal) || (!internal && (!provider || id.Internal)) {
			problem(w, 403, "FORBIDDEN")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		var v int
		err := s.Store.Pool.QueryRow(r.Context(), "SELECT version FROM schema_version WHERE version=9").Scan(&v)
		if err != nil {
			fail(w, err)
			return
		}
		if err = s.Consumer.Ready(r.Context()); err != nil {
			fail(w, err)
			return
		}
		if err = s.Publisher.Ready(r.Context()); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]string{"database": "ready", "messaging": "ready"})
	})
	mux.HandleFunc("GET /metrics", s.secure(true, s.metrics))
	mux.HandleFunc("POST /wallets", s.secure(true, s.open))
	mux.HandleFunc("GET /wallets/{walletId}", s.secure(true, s.wallet))
	mux.HandleFunc("GET /wallets/{walletId}/ledger", s.secure(true, s.ledger))
	mux.HandleFunc("POST /wallets/{walletId}/reconciliation", s.secure(true, s.reconcile))
	mux.HandleFunc("POST /wagering/transactions", s.secure(false, s.process))
	mux.HandleFunc("GET /wagering/transactions/{transactionId}", s.secure(false, s.transaction))
	mux.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalId}", s.secure(false, s.transaction))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Requests.Add(1)
		traced, span := s.Store.Tracing.Start(telemetry.Extract(r.Context(), propagation.HeaderCarrier(r.Header)), "HTTP", trace.SpanKindServer)
		defer span.End()
		r = r.WithContext(traced)
		start := time.Now()
		cid := r.Header.Get("X-Correlation-Id")
		if len(cid) > 100 || cid == "" {
			cid = uuid.NewString()
		}
		w.Header().Set("X-Correlation-Id", cid)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		recorder := &statusWriter{ResponseWriter: w}
		r = r.WithContext(store.WithTrace(ctx, cid, ""))
		mux.ServeHTTP(recorder, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		span.SetName(route)
		span.SetAttributes(attribute.String("http.route", route), attribute.String("http.request.method", r.Method), attribute.Int("http.response.status_code", recorder.status))
		if recorder.status >= 500 {
			span.SetStatus(codes.Error, "server error")
		}
		s.DurationMicros.Add(time.Since(start).Microseconds())
		if recorder.status == 409 {
			s.Conflicts.Add(1)
		}
		slog.Info("http request", "method", r.Method, "correlationId", cid, "durationMs", time.Since(start).Milliseconds())
	})
}
func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlayerID string           `json:"playerId"`
		Initial  domain.MoneyJSON `json:"initialBalance"`
	}
	if err := decode(w, r, &req); err != nil {
		fail(w, err)
		return
	}
	player, err := uuidString(req.PlayerID)
	if err != nil {
		fail(w, err)
		return
	}
	m, err := domain.ParseMoney(req.Initial)
	if err != nil {
		fail(w, err)
		return
	}
	v, err := s.Store.Open(r.Context(), player, m)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 201, v)
}
func (s *Server) wallet(w http.ResponseWriter, r *http.Request) {
	id, err := uuidString(r.PathValue("walletId"))
	if err != nil {
		fail(w, err)
		return
	}
	v, err := s.Store.Wallet(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, v)
}
func (s *Server) process(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		problem(w, 400, "INVALID_IDEMPOTENCY_KEY")
		return
	}
	var o domain.Operation
	if err := decode(w, r, &o); err != nil {
		fail(w, err)
		return
	}
	id := r.Context().Value(identityKey{}).(identity)
	if o.ProviderID != id.Provider {
		problem(w, 403, "PROVIDER_MISMATCH")
		return
	}
	var err error
	o.WalletID, err = uuidString(o.WalletID)
	if err != nil {
		fail(w, err)
		return
	}
	o.PlayerID, err = uuidString(o.PlayerID)
	if err != nil {
		fail(w, err)
		return
	}
	if _, err = o.Validate(); err != nil {
		fail(w, err)
		return
	}
	v, err := s.Store.Process(r.Context(), o, key)
	if err != nil {
		fail(w, err)
		return
	}
	if v.Replay {
		s.Replays.Add(1)
	}
	slog.Info("transaction result", "correlationId", w.Header().Get("X-Correlation-Id"), "transactionId", v.TransactionID, "walletId", o.WalletID, "providerId", o.ProviderID, "status", v.Status, "replay", v.Replay)
	status := 200
	if v.Status == "PENDING_REFERENCE" {
		status = 202
	}
	if v.Status == "FAILED" {
		status = 500
	}
	if v.Status == "REJECTED" {
		status = 422
	}
	reply(w, status, v)
}
func (s *Server) transaction(w http.ResponseWriter, r *http.Request) {
	auth := r.Context().Value(identityKey{}).(identity)
	provider := r.PathValue("providerId")
	if provider != "" && provider != auth.Provider {
		problem(w, 404, "NOT_FOUND")
		return
	}
	id := r.PathValue("transactionId")
	if id != "" {
		var err error
		id, err = uuidString(id)
		if err != nil {
			fail(w, err)
			return
		}
	}
	v, err := s.Store.Transaction(r.Context(), auth.Provider, id, r.PathValue("externalId"))
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, v)
}

type cursor struct {
	Wallet string `json:"w"`
	Seq    int64  `json:"s"`
}

func (s *Server) ledger(w http.ResponseWriter, r *http.Request) {
	id, err := uuidString(r.PathValue("walletId"))
	if err != nil {
		fail(w, err)
		return
	}
	limit := 50
	if n := r.URL.Query().Get("limit"); n != "" {
		limit, err = strconv.Atoi(n)
		if err != nil || limit < 1 || limit > 100 {
			fail(w, domain.ErrInvalid)
			return
		}
	}
	c := cursor{Wallet: id}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || json.Unmarshal(b, &c) != nil || c.Wallet != id || c.Seq < 0 {
			fail(w, domain.ErrInvalid)
			return
		}
	}
	entries, err := s.Store.Ledger(r.Context(), id, c.Seq, limit+1)
	if err != nil {
		fail(w, err)
		return
	}
	next := ""
	if len(entries) > limit {
		entries = entries[:limit]
		b, _ := json.Marshal(cursor{id, entries[len(entries)-1].Sequence})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	reply(w, 200, map[string]any{"items": entries, "nextCursor": next})
}
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	id, err := uuidString(r.PathValue("walletId"))
	if err != nil {
		fail(w, err)
		return
	}
	v, err := s.Store.Reconcile(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if !v.Consistent {
		s.Divergences.Add(1)
		slog.Error("reconciliation divergence", "walletId", id)
	}
	reply(w, 200, v)
}
