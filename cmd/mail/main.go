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
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/mailcache"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmcp"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
	"github.com/olegpustovit/mcp-icloud-email/web"
)

var version = "dev"

type principalKey struct{}

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
	cacheCtx, cacheCancel := context.WithTimeout(ctx, 20*time.Second)
	cache, err := mailcache.New(cacheCtx, cfg.MongoURI, "mail")
	cacheCancel()
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = cache.Close(c)
	}()
	handler, err := newHandler(service, cache)
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
		email, err := authenticate(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource", scope="mail"`, baseURL))
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, email)))
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("http request", "method", r.Method, "path", r.URL.Path, "user_agent", r.UserAgent())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

func newHandler(service *auth.Service, cache *mailcache.Store) (http.Handler, error) {
	mux := http.NewServeMux()
	authHandler := authhttp.New(service)
	authHandler.InvalidateAccount = cache.InvalidateOwner
	authHandler.Register(mux)
	server := mailmcp.New(func(ctx context.Context) (mailmcp.Dependencies, error) {
		email, _ := ctx.Value(principalKey{}).(string)
		if email == "" {
			return mailmcp.Dependencies{}, auth.ErrUnauthorized
		}
		address, password, err := service.Credentials(ctx, email)
		if err != nil {
			return mailmcp.Dependencies{}, errors.New("connect your iCloud account at " + service.Config.BaseURL)
		}
		client, err := imap.NewClient(address, password)
		if err != nil {
			return mailmcp.Dependencies{}, errors.New("iCloud connection failed; check your app-specific password")
		}
		return mailmcp.Dependencies{IMAP: cache.Wrap(client, email, address), SMTP: smtp.NewClient(address, password), FromEmail: address, Close: func() { _ = client.Close() }}, nil
	}, version)
	// The reverse proxy preserves the public Host; protect enforces that exact host and origin.
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true, PropagateRequestCancellation: true, MaxRequestBodyBytes: 12 << 20})
	mux.Handle("/mcp", protect(service.Authenticate, service.Config.BaseURL, transport))
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
