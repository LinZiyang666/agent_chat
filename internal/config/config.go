// Package config loads daemon and CLI configuration. The data root
// defaults to ~/.agentchat and is overridable via $AGENTCHAT_HOME or a
// command-line flag. A TOML file at <data_root>/config.toml overrides
// the defaults; environment variables override the file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// Config is the resolved daemon configuration. All paths are absolute
// once Load returns successfully.
type Config struct {
	// DataRoot is the on-disk directory where the daemon keeps its state.
	DataRoot string `toml:"data_root"`
	// SocketPath is where agentchatd listens for CLI requests.
	SocketPath string `toml:"socket_path"`
	// DBPath is the SQLite database file path.
	DBPath string `toml:"db_path"`
	// KeyPath is the AES master-key file. Used to encrypt Discord bot
	// tokens at rest; bootstrapped on first daemon start.
	KeyPath string `toml:"key_path"`
	// Log is the logging configuration block.
	Log LogConfig `toml:"log"`
	// Discord is the Discord-backend configuration block. Optional in
	// M3 (lifecycle only); required by M4 once a room is created
	// (rooms need a guild_id to live in).
	Discord DiscordConfig `toml:"discord"`
}

// LogConfig configures the daemon's slog output.
type LogConfig struct {
	// Level is one of "debug", "info", "warn", "error".
	Level string `toml:"level"`
}

// DiscordConfig configures the Discord adapter. Per requirements §3.2,
// agentchat targets a single Discord guild — multi-guild is explicitly
// out of scope.
type DiscordConfig struct {
	// GuildID is the Discord guild that all agentchat rooms live in.
	// Empty means "not configured": online still works, but room.create
	// (and any other guild-scoped operation) fails with InvalidArgument
	// until set.
	GuildID string `toml:"guild_id"`
}

const (
	// EnvHome overrides DataRoot.
	EnvHome = "AGENTCHAT_HOME"
	// EnvSocket overrides SocketPath.
	EnvSocket = "AGENTCHAT_SOCKET"
	// EnvLogLevel overrides Log.Level.
	EnvLogLevel = "AGENTCHAT_LOG_LEVEL"
	// EnvDiscordGuildID overrides Discord.GuildID.
	EnvDiscordGuildID = "AGENTCHAT_DISCORD_GUILD_ID"

	// configFileName is the TOML config file expected inside DataRoot.
	configFileName = "config.toml"

	// DataRootMode is the permission mode enforced on the data root
	// directory. It is enforced on every daemon start, including when
	// the directory already exists with broader perms (fix for M2-P3-005).
	DataRootMode os.FileMode = 0o700
)

// Defaults returns the built-in defaults rooted at the given data dir.
// Pass an empty dataRoot to use ~/.agentchat.
func Defaults(dataRoot string) Config {
	if dataRoot == "" {
		dataRoot = defaultDataRoot()
	}
	return Config{
		DataRoot:   dataRoot,
		SocketPath: filepath.Join(dataRoot, "agentchatd.sock"),
		DBPath:     filepath.Join(dataRoot, "agentchatd.db"),
		KeyPath:    filepath.Join(dataRoot, "master.key"),
		Log:        LogConfig{Level: "info"},
	}
}

// Load resolves the daemon configuration. Precedence (highest first):
//
//  1. environment variables (AGENTCHAT_SOCKET, AGENTCHAT_LOG_LEVEL)
//  2. <data_root>/config.toml
//  3. built-in defaults
//
// dataRoot, if non-empty, overrides $AGENTCHAT_HOME and the default,
// and determines where Load looks for config.toml.
//
// TOML data_root rebasing (fix for M2-P3-006): if config.toml sets
// data_root to a value different from the resolved root, the derived
// defaults (SocketPath, DBPath, KeyPath) are recomputed against the
// new root *before* any explicit TOML overrides are applied. This way
// a config that sets only data_root produces a consistent path set.
func Load(dataRoot string) (Config, error) {
	if dataRoot == "" {
		dataRoot = os.Getenv(EnvHome)
	}
	if dataRoot == "" {
		dataRoot = defaultDataRoot()
	}
	abs, err := filepath.Abs(dataRoot)
	if err != nil {
		return Config{}, errcode.Wrap(err, errcode.Internal, "absolute path for %s", dataRoot)
	}
	dataRoot = abs

	overlay, ok, err := readOverlayTOML(dataRoot)
	if err != nil {
		return Config{}, err
	}

	// If the file's data_root differs, re-derive defaults from THAT.
	effectiveRoot := dataRoot
	if ok && overlay.DataRoot != "" && overlay.DataRoot != dataRoot {
		newRoot, err := filepath.Abs(overlay.DataRoot)
		if err != nil {
			return Config{}, errcode.Wrap(err, errcode.InvalidArgument,
				"absolute path for data_root in config")
		}
		effectiveRoot = newRoot
	}
	cfg := Defaults(effectiveRoot)

	// Apply only non-empty TOML fields so unspecified keys keep their
	// freshly-rebased defaults.
	if ok {
		if overlay.SocketPath != "" {
			cfg.SocketPath = overlay.SocketPath
		}
		if overlay.DBPath != "" {
			cfg.DBPath = overlay.DBPath
		}
		if overlay.KeyPath != "" {
			cfg.KeyPath = overlay.KeyPath
		}
		if overlay.Log.Level != "" {
			cfg.Log.Level = overlay.Log.Level
		}
		if overlay.Discord.GuildID != "" {
			cfg.Discord.GuildID = overlay.Discord.GuildID
		}
	}

	// Env wins over defaults and TOML.
	if v := os.Getenv(EnvSocket); v != "" {
		cfg.SocketPath = v
	}
	if v := os.Getenv(EnvLogLevel); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv(EnvDiscordGuildID); v != "" {
		cfg.Discord.GuildID = v
	}

	if err := cfg.finalize(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// defaultDataRoot returns ~/.agentchat, falling back to ./.agentchat if
// the user's home cannot be determined (e.g. in a stripped container).
func defaultDataRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".agentchat"
	}
	return filepath.Join(home, ".agentchat")
}

// readOverlayTOML reads <dataRoot>/config.toml if it exists. The
// returned bool is true when a file was loaded.
func readOverlayTOML(dataRoot string) (Config, bool, error) {
	path := filepath.Join(dataRoot, configFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, false, nil
		}
		return Config{}, false, errcode.Wrap(err, errcode.Internal, "read config file %s", path)
	}
	var overlay Config
	if _, err := toml.Decode(string(data), &overlay); err != nil {
		return Config{}, false, errcode.Wrap(err, errcode.InvalidArgument, "parse config file %s", path)
	}
	return overlay, true, nil
}

// finalize ensures paths are absolute and Log.Level has a value.
func (c *Config) finalize() error {
	if c.DataRoot == "" {
		return errcode.New(errcode.InvalidArgument, "DataRoot is empty")
	}
	abs, err := filepath.Abs(c.DataRoot)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "absolute path for %s", c.DataRoot)
	}
	c.DataRoot = abs

	// Any path the user provided as relative gets rooted under DataRoot
	// (config files are not expected to use relative paths, but be safe).
	if !filepath.IsAbs(c.SocketPath) {
		c.SocketPath = filepath.Join(c.DataRoot, c.SocketPath)
	}
	if !filepath.IsAbs(c.DBPath) {
		c.DBPath = filepath.Join(c.DataRoot, c.DBPath)
	}
	if !filepath.IsAbs(c.KeyPath) {
		c.KeyPath = filepath.Join(c.DataRoot, c.KeyPath)
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	return nil
}

// EnsureDataRoot creates the data root directory (and parents) if it
// does not yet exist, and enforces DataRootMode (0o700) on it whether
// it was just created or already existed. Fix for M2-P3-005: a
// pre-existing wide-perm dir is now tightened rather than silently
// left exposed.
func (c Config) EnsureDataRoot() error {
	if err := os.MkdirAll(c.DataRoot, DataRootMode); err != nil {
		return errcode.Wrap(err, errcode.Internal, "create data root %s", c.DataRoot)
	}
	if err := os.Chmod(c.DataRoot, DataRootMode); err != nil {
		return errcode.Wrap(err, errcode.Internal, "tighten data root %s permissions", c.DataRoot)
	}
	return nil
}

// String renders a one-line summary; useful for daemon-start logs.
func (c Config) String() string {
	return fmt.Sprintf("DataRoot=%s Socket=%s DB=%s LogLevel=%s",
		c.DataRoot, c.SocketPath, c.DBPath, c.Log.Level)
}
