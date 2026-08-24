// Package config resolves the plugin's directories and reads its TOML config
// (§5.1, §10). Config never holds a secret: a value that needs one names a
// file path or an environment variable and is dereferenced at use (§10.2).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// Name is the plugin's short name, and the one name everything nameable is
// derived from: the state dir, the config dir, the socket, the lock, the log,
// the store and the §9.4 gate verbs. The BINARY is hsched, because §13.1
// forbids a name that collides with a common Unix command and `sched` is too
// close to one to spend.
const Name = "sched"

// EnvPrefix is the environment override prefix. §10.1 fixes it as `<NAME>_`,
// the uppercase short name, so this plugin's is SCHED_.
//
// A variable this plugin HANDS to something else is prefixed by the binary
// name instead, because the reader knows the binary and not the short name.
// There is no such variable yet; the day there is, it is HSCHED_.
const EnvPrefix = "SCHED_"

// PluginID is the id Herdr knows this plugin by (§13.1), which is also the
// directory Herdr keeps plugin state under. Nothing is stored there; it is
// named so doctor can spot a store left behind at that path.
const PluginID = "herdr-sched"

// DefaultTickSeconds is the bounded timer of §11.5: how often the daemon wakes
// to do its own reconciliation. There is nothing domain-specific on the tick
// yet, and the timer exists from day one because a daemon that grows one later
// should not also be growing its first timer then.
const DefaultTickSeconds = 30

// One plugin, one store. Herdr injects HERDR_PLUGIN_STATE_DIR and
// HERDR_PLUGIN_CONFIG_DIR into what IT spawns — startup, actions, popup panes —
// and injects neither into a managed pane, where the agents run. Honouring
// them would give one plugin two stores that never see each other's rows.
// Since contract 0.5.0, not reading them is §5.1 and §10.1's own rule.
//
// SCHED_STATE_DIR and SCHED_CONFIG_DIR are the plugin-owned overrides. They
// are how tests isolate (§12.3), and how an operator asks for a second store
// on purpose rather than by accident.

// StateDir is SCHED_STATE_DIR, else ${XDG_STATE_HOME:-~/.local/state}/sched.
func StateDir() string {
	return dirFrom(EnvPrefix+"STATE_DIR", "XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// ConfigDir is SCHED_CONFIG_DIR, else ${XDG_CONFIG_HOME:-~/.config}/sched.
func ConfigDir() string {
	return dirFrom(EnvPrefix+"CONFIG_DIR", "XDG_CONFIG_HOME", ".config")
}

// dirFrom resolves one directory: the plugin's own override, else the XDG base
// with the plugin's name under it, else the same layout under the home dir.
func dirFrom(own, xdg, home string) string {
	if d := os.Getenv(own); d != "" {
		return d
	}
	if d := os.Getenv(xdg); d != "" {
		return filepath.Join(d, Name)
	}
	return filepath.Join(homeDir(), home, Name)
}

// SocketPath is <state dir>/sched.sock (§2.2).
func SocketPath() string { return filepath.Join(StateDir(), Name+".sock") }

// LockPath is <state dir>/sched.lock, the file whose flock elects the one
// daemon per store (§2.3). It is held for the daemon's lifetime and released
// by the kernel when the process ends, so a crash leaves nothing to clean up.
func LockPath() string { return filepath.Join(StateDir(), Name+".lock") }

// LogPath is <state dir>/sched.log, where a daemon with no terminal writes.
func LogPath() string { return filepath.Join(StateDir(), Name+".log") }

// StorePath is <state dir>/sched.json, the one document this daemon keeps
// across restarts (§5.1).
func StorePath() string { return filepath.Join(StateDir(), Name+".json") }

// ConfigPath is <config dir>/sched.toml (§10.1). One config path per plugin,
// and no other: a file under a different directory or a different extension is
// a leftover, not an alternative.
func ConfigPath() string { return filepath.Join(ConfigDir(), Name+".toml") }

// OrphanStoreDirs lists the directories a second sched.json could be sitting
// in because a build resolved the store from Herdr's injected dirs. It is
// detection, never deletion. The store actually in use is never listed, so
// doctor can never point at live data.
func OrphanStoreDirs() []string {
	var out []string
	inUse := StateDir()
	add := func(dir string) {
		if dir == "" || dir == inUse {
			return
		}
		for _, seen := range out {
			if seen == dir {
				return
			}
		}
		out = append(out, dir)
	}
	add(os.Getenv("HERDR_PLUGIN_STATE_DIR"))
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(homeDir(), ".local", "state")
	}
	add(filepath.Join(base, "herdr", "plugins", PluginID))
	return out
}

// EnsureStateDir creates the state dir with mode 0700 (§3.5). It tightens a
// dir that already exists more widely rather than accepting it: the boundary
// is the local user account, and a schedule another account can rewrite is not
// that boundary.
func EnsureStateDir() error {
	dir := StateDir()
	// A relative path here means nothing named the store: no SCHED_STATE_DIR,
	// no XDG_STATE_HOME and no resolvable home. Creating it anyway would put
	// one store under every working directory the binary is run from.
	if !filepath.IsAbs(dir) {
		return codes.Errorf(codes.Unavailable,
			"cannot place the store: %sSTATE_DIR resolves to %q, which is not an absolute path; set %sSTATE_DIR or HOME",
			EnvPrefix, dir, EnvPrefix)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return codes.Errorf(codes.Unavailable, "create the state dir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return codes.Errorf(codes.Unavailable, "restrict %s to this user: %v", dir, err)
	}
	return nil
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// Config is the whole of what the plugin reads at daemon start (§10.1). It is
// read ONCE, at startup: the daemon holds the document it started with, and a
// change to the file takes effect on the next start.
type Config struct {
	// TickSeconds is the bounded timer of §11.5.
	TickSeconds int64 `json:"tick_seconds"`
	// GateCommand is the policy gate of §9.2. Empty means unconfigured, which
	// means allow.
	GateCommand []string `json:"gate_command"`
	// OnEvent is the §8.3 hook, run detached with all three stdio closed.
	OnEvent []string `json:"on_event"`
	// Path is where this came from, for doctor.
	Path string `json:"path"`
	// Present says whether the file existed at all.
	Present bool `json:"present"`
}

// Keys is every key the config document accepts, in the order the README
// lists them. A test reads one against the other, so a key added here without
// a line in Configuration is a failing test rather than an undocumented knob.
var Keys = []string{"tick_seconds", "gate_command", "on_event"}

// Load reads the config file, applies defaults, then applies SCHED_ overrides.
// A missing file is the unconfigured default; a malformed one is an error,
// because silently falling back would turn a typo in the gate command into an
// open gate, and §9.2 fails closed.
func Load() (*Config, error) { return LoadFrom(ConfigPath()) }

// LoadFrom is Load against a named file, which is what `hsched daemon
// --config` passes and what a test passes.
func LoadFrom(path string) (*Config, error) {
	c := &Config{TickSeconds: DefaultTickSeconds, Path: path}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		// The environment, not the caller: §6.3's UNAVAILABLE, exit 4. An
		// uncoded error reaches the CLI as USAGE, which tells a caller its
		// command line was wrong about a file it could not read.
		return nil, codes.Errorf(codes.Unavailable, "read %s: %v", path, err)
	default:
		c.Present = true
		kv, err := parseTOML(string(raw))
		if err != nil {
			return nil, codes.Errorf(codes.Usage, "%s: %v", path, err)
		}
		for k, v := range kv {
			if err := c.apply(k, v); err != nil {
				return nil, codes.Errorf(codes.Usage, "%s: %v", path, err)
			}
		}
	}
	for _, k := range Keys {
		if v, ok := os.LookupEnv(EnvPrefix + strings.ToUpper(k)); ok && v != "" {
			if err := c.apply(k, value{scalar: v, isList: true, list: strings.Fields(v)}); err != nil {
				return nil, codes.Errorf(codes.Usage, "%s%s: %v", EnvPrefix, strings.ToUpper(k), err)
			}
		}
	}
	return c, nil
}

func (c *Config) apply(key string, v value) error {
	switch key {
	case "tick_seconds":
		n, err := strconv.ParseInt(strings.TrimSpace(v.scalar), 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("tick_seconds must be a positive number, got %q", v.scalar)
		}
		c.TickSeconds = n
	case "gate_command":
		// A scalar here would assign nil and the gate would silently read as
		// unconfigured — every write allowed by a typo. Refusing to load is
		// the only fail-closed answer a config parser has (§9.2).
		if !v.isList {
			return fmt.Errorf("gate_command must be an array of strings, got %q", v.scalar)
		}
		c.GateCommand = v.list
	case "on_event":
		if !v.isList {
			return fmt.Errorf("on_event must be an array of strings, got %q", v.scalar)
		}
		c.OnEvent = v.list
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return nil
}
