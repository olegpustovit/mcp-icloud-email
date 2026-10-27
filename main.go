package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/olegpustovit/mcp-icloud-email/config"
	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmcp"
	"github.com/olegpustovit/mcp-icloud-email/smtp"
)

// version is set at build time via ldflags.
var version = "dev"

func main() {
	logLevel := new(slog.LevelVar)
	logLevel.Set(slog.LevelInfo)
	switch strings.ToUpper(os.Getenv("LOG_LEVEL")) {
	case "DEBUG":
		logLevel.Set(slog.LevelDebug)
	case "WARN":
		logLevel.Set(slog.LevelWarn)
	case "ERROR":
		logLevel.Set(slog.LevelError)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}

	imapClient, err := imap.NewClient(cfg.ICloudEmail, cfg.ICloudPassword)
	if err != nil {
		slog.Error("failed to create IMAP client", "error", err)
		os.Exit(1)
	}
	if _, err := imapClient.ListFolders(context.Background()); err != nil {
		_ = imapClient.Close()
		slog.Error("failed to connect to iCloud IMAP (check credentials)", "error", err)
		os.Exit(1)
	}
	defer func() { _ = imapClient.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	smtpClient := smtp.NewClient(cfg.ICloudEmail, cfg.ICloudPassword)
	server := mailmcp.New(func(context.Context) (mailmcp.Dependencies, error) {
		return mailmcp.Dependencies{IMAP: imapClient, SMTP: smtpClient, FromEmail: cfg.ICloudEmail}, nil
	}, version)
	slog.Info("server starting", "version", version, "email", cfg.ICloudEmail,
		"imap_server", fmt.Sprintf("imap.mail.me.com:%d", 993), "smtp_server", fmt.Sprintf("smtp.mail.me.com:%d", 587))
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		slog.Error("server error", "error", err)
		return
	}
	slog.Info("server stopped")
}
