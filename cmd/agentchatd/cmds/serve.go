package cmds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/LinZiyang666/agentchat/internal/account"
	"github.com/LinZiyang666/agentchat/internal/api"
	"github.com/LinZiyang666/agentchat/internal/audit"
	"github.com/LinZiyang666/agentchat/internal/auth"
	"github.com/LinZiyang666/agentchat/internal/bot"
	"github.com/LinZiyang666/agentchat/internal/bot/discord"
	"github.com/LinZiyang666/agentchat/internal/config"
	"github.com/LinZiyang666/agentchat/internal/connector"
	"github.com/LinZiyang666/agentchat/internal/crypto"
	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/message"
	"github.com/LinZiyang666/agentchat/internal/state"
	"github.com/LinZiyang666/agentchat/internal/store/sqlite"
)

var (
	serveDataRoot string
	serveSocket   string
	serveLogLevel string
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the agentchatd daemon in the foreground.",
	Long: `serve starts the long-running daemon. It listens on a Unix
domain socket for CLI requests, owns the SQLite store, and (from M3
onwards) maintains Discord bot connections.

On first run with an empty database, serve creates an admin account
named "root" and prints a one-time API token. Capture this token
immediately — it cannot be retrieved later.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runServe(cmd.Context())
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveDataRoot, "data-root", "",
		"data directory (overrides $AGENTCHAT_HOME, default ~/.agentchat)")
	serveCmd.Flags().StringVar(&serveSocket, "socket", "",
		"Unix socket path (default <data-root>/agentchatd.sock)")
	serveCmd.Flags().StringVar(&serveLogLevel, "log-level", "",
		"log level: debug, info, warn, error (default info)")
	rootCmd.AddCommand(serveCmd)
}

func runServe(ctx context.Context) error {
	cfg, err := config.Load(serveDataRoot)
	if err != nil {
		return err
	}
	if serveSocket != "" {
		cfg.SocketPath = serveSocket
	}
	if serveLogLevel != "" {
		cfg.Log.Level = serveLogLevel
	}
	if err := cfg.EnsureDataRoot(); err != nil {
		return err
	}

	// Acquire the single-instance lock BEFORE we touch the socket or
	// SQLite WAL files. A second daemon hitting this point is rejected
	// here with errcode.Conflict (fix for M2-P3-001).
	lock, err := acquireDataRootLock(filepath.Join(cfg.DataRoot, dataRootLockName))
	if err != nil {
		return err
	}
	defer lock.Release()

	log := newLogger(cfg.Log.Level)
	log.Info("starting", "version", Version, "config", cfg.String())

	masterKey, err := crypto.LoadOrCreateMasterKey(cfg.KeyPath)
	if err != nil {
		return err
	}

	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	bundle := db.Bundle()
	accountSvc := account.NewService(bundle.Accounts)
	auditRec := audit.NewRecorder(bundle.Audit)
	authMgr := auth.NewManager(bundle.Tokens)
	// Discord-backed Provider factory; future Matrix/Slack adapters
	// would swap this in test or via config.
	conn := connector.New(func(token string, hint bot.Identity) bot.Provider {
		return discord.New(token, hint, discord.Options{
			GuildID: cfg.Discord.GuildID,
		})
	}, log)
	defer conn.Shutdown(context.Background())

	// M5 state-fan-out engine. The aggregator depends on the Bundle
	// + a Connector.Status reader for the health bar; the bus owns
	// the version counter, debouncer, and subscriber set.
	agg := state.NewFromConnector(db.Bundle(), conn)
	stateBus := state.NewBus(agg, log)
	defer stateBus.Shutdown()

	// M4 inbound-message ingester. Drained by per-account background
	// goroutines that the OnlineAccount handler attaches on success
	// and OfflineAccount detaches on teardown. The ingester also
	// publishes to the state bus so watchers see new inbound
	// messages.
	ingester := message.New(conn, db, log, stateBus)

	if err := bootstrapRoot(ctx, accountSvc, authMgr); err != nil {
		return err
	}

	if err := removeStaleSocket(cfg.SocketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "listen on %s", cfg.SocketPath)
	}
	defer listener.Close()
	// 0o600: only the owning user may connect. The data root is itself
	// 0o700 so this is defense in depth, but explicit > implicit.
	if err := os.Chmod(cfg.SocketPath, 0o600); err != nil {
		return errcode.Wrap(err, errcode.Internal, "chmod socket")
	}

	handler := api.NewRouter(api.Deps{
		Log:         log,
		Accounts:    accountSvc,
		AccountRepo: bundle.Accounts,
		TokenRepo:   bundle.Tokens,
		Auth:        authMgr,
		Audit:       auditRec,
		Bundler:     db, // *sqlite.Store implements store.Bundler
		Connector:   conn,
		MasterKey:   masterKey,
		Ingester:    ingester,
		StateBus:    stateBus,
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Allow long-poll endpoints to land later (status view in M5)
		// without immediate timeout strangling them.
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
		BaseContext:  func(_ net.Listener) context.Context { return ctx },
	}

	shutdownCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "socket", cfg.SocketPath)
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- errcode.Wrap(err, errcode.Internal, "http serve")
			return
		}
		errCh <- nil
	}()

	select {
	case <-shutdownCtx.Done():
		log.Info("shutdown signal received")
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel2()
		if err := srv.Shutdown(ctx2); err != nil {
			log.Warn("graceful shutdown failed", "err", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// bootstrapRoot ensures the admin account exists; on first run it
// prints a one-time API token to stdout (NOT stderr, so the operator
// can pipe it into `read` or `grep`).
func bootstrapRoot(ctx context.Context, accounts *account.Service, mgr *auth.Manager) error {
	root, created, err := accounts.BootstrapRoot(ctx)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	raw, _, err := mgr.Issue(ctx, root.ID)
	if err != nil {
		return err
	}
	// Stand-out banner; this is the only chance to see the token.
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("================================================================\n")
	b.WriteString("  First-time setup: created admin account 'root'.\n")
	b.WriteString("\n")
	b.WriteString("  Save this API token NOW — it will not be shown again:\n")
	b.WriteString("\n")
	b.WriteString("    AGENTCHAT_TOKEN=")
	b.WriteString(raw)
	b.WriteString("\n")
	b.WriteString("================================================================\n")
	b.WriteString("\n")
	if _, err := fmt.Fprint(os.Stdout, b.String()); err != nil {
		return errcode.Wrap(err, errcode.Internal, "write bootstrap banner")
	}
	return nil
}

// removeStaleSocket removes a leftover socket file if one exists. A
// non-socket file blocks startup so the daemon never deletes user data
// by accident.
func removeStaleSocket(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errcode.Wrap(err, errcode.Internal, "stat socket")
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errcode.New(errcode.Conflict,
			"socket path %s exists and is not a socket; refusing to remove", path)
	}
	if err := os.Remove(path); err != nil {
		return errcode.Wrap(err, errcode.Internal, "remove stale socket")
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
