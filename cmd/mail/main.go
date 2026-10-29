package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/cmd/mail/authhttp"
	"github.com/olegpustovit/mcp-icloud-email/cmd/mail/mailhttp"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/mailsync"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmcp"
	"github.com/olegpustovit/mcp-icloud-email/web"
)

var version = "dev"

type principalKey struct{}
type actorKey struct{}

func main() {
	if err := run(); err != nil {
		slog.Error("mail server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cfg := auth.ConfigFromEnv()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	service, err := auth.New(startup, cfg)
	cancel()
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = service.Close(closeCtx)
	}()
	syncService, err := mailsync.New(ctx, cfg.MongoURI, cfg.EncryptionKey, service.Credentials)
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		_ = syncService.Close(c)
	}()
	owners, err := service.ConnectedOwners(ctx)
	if err != nil {
		return fmt.Errorf("load connected mail owners: %w", err)
	}
	for _, owner := range owners {
		if !auth.Allowed(owner) {
			continue
		}
		if _, err := syncService.Start(ctx, owner); err != nil {
			slog.Warn("resume connected mailbox", "owner", owner, "error", err)
		}
	}
	handler, err := newHandler(service, syncService)
	if err != nil {
		return err
	}
	addr := os.Getenv("MAIL_ADDR")
	if addr == "" {
		addr = "127.0.0.1:4290"
	}
	httpServer := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	done := make(chan error, 1)
	go func() { slog.Info("mail server listening", "address", addr); done <- httpServer.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}

func protect(authenticate func(*http.Request) (string, error), baseURL string, next http.Handler) http.Handler {
	return protectPrincipal(func(r *http.Request) (string, string, error) {
		email, err := authenticate(r)
		actor := "web"
		if cookie, e := r.Cookie("__Host-mail_session"); e == nil {
			actor += ":" + auth.Hash(cookie.Value)[:12]
		}
		return email, actor, err
	}, baseURL, next)
}
func protectPrincipal(authenticate func(*http.Request) (string, string, error), baseURL string, next http.Handler) http.Handler {
	expected, _ := url.Parse(baseURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if expected == nil || r.Host != expected.Host {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != baseURL {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		email, actor, err := authenticate(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="mail"`, baseURL))
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
		ctx := context.WithValue(r.Context(), principalKey{}, email)
		ctx = context.WithValue(ctx, actorKey{}, actor)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("http request", "method", r.Method, "path", r.URL.Path, "user_agent", r.UserAgent())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self' data:; img-src 'self' data: blob:; connect-src 'self'; frame-src 'self' blob:; worker-src 'self'; manifest-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

func newHandler(service *auth.Service, state *mailsync.Service) (http.Handler, error) {
	mux := http.NewServeMux()
	authHandler := authhttp.New(service)
	if state != nil {
		authHandler.ValidateAccount = state.ValidateAccount
		state.ConfigurePush(service.ActiveSession, service.Config.BaseURL)
	}
	authHandler.InvalidateAccount = func(ctx context.Context, owner string, disconnect bool) error {
		if state != nil {
			return state.Invalidate(ctx, owner, disconnect)
		}
		return nil
	}
	authHandler.Register(mux)
	resolve := func(ctx context.Context) (mailmcp.Dependencies, error) {
		email, _ := ctx.Value(principalKey{}).(string)
		if email == "" {
			return mailmcp.Dependencies{}, auth.ErrUnauthorized
		}
		if state != nil {
			actor, _ := ctx.Value(actorKey{}).(string)
			mailbox, err := state.Mailbox(ctx, email, actor)
			if err != nil {
				return mailmcp.Dependencies{}, err
			}
			address, err := state.Account(ctx, email)
			if err != nil {
				return mailmcp.Dependencies{}, err
			}
			return mailmcp.Dependencies{IMAP: mailbox, SMTP: mailbox, FromEmail: address}, nil
		}
		return mailmcp.Dependencies{}, errors.New("mail sync is unavailable")
	}
	server := mailmcp.New(resolve, version)
	webAPI := protect(authHandler.Session, service.Config.BaseURL, mailhttp.New(resolve, service.Config.BaseURL, state, func(ctx context.Context) string { owner, _ := ctx.Value(principalKey{}).(string); return owner }, authHandler.Session))
	mux.Handle("/api/mail/", webAPI)
	mux.Handle("/api/sync/", webAPI)
	// The reverse proxy preserves the public Host; protect enforces that exact host and origin.
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true, PropagateRequestCancellation: true, MaxRequestBodyBytes: 12 << 20})
	mux.Handle("/mcp", protectPrincipal(service.AuthenticateClient, service.Config.BaseURL, transport))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	assets, err := fs.Sub(web.FS, "dist")
	if err != nil {
		return nil, err
	}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		http.FileServer(http.FS(assets)).ServeHTTP(w, r)
	}))

	return securityHeaders(mux), nil
}
