package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-sched/internal/codes"
)

// §5.1 and §10.1: the short name governs everything nameable, and everything derived
// from it is derived rather than spelled out a second time. The name is
// `sched` (§13.2).
func TestEveryPathIsDerivedFromTheShortName(t *testing.T) {
	state := t.TempDir()
	config := t.TempDir()
	t.Setenv(EnvPrefix+"STATE_DIR", state)
	t.Setenv(EnvPrefix+"CONFIG_DIR", config)

	for what, got := range map[string]string{
		"socket": SocketPath(),
		"lock":   LockPath(),
		"log":    LogPath(),
		"store":  StorePath(),
	} {
		if filepath.Dir(got) != state {
			t.Errorf("the %s is at %s, outside the state dir %s", what, got, state)
		}
		if !strings.HasPrefix(filepath.Base(got), Name+".") {
			t.Errorf("the %s is %s and is not named for the short name %q", what, got, Name)
		}
	}
	// §10.1: one config path per plugin, and no other.
	if want := filepath.Join(config, Name+".toml"); ConfigPath() != want {
		t.Errorf("config path = %s, want %s", ConfigPath(), want)
	}
}

// The XDG bases put the short name under them, which is what makes an operator
// who knows one sibling able to guess this one's directories.
func TestTheXDGBasesCarryTheShortName(t *testing.T) {
	t.Setenv(EnvPrefix+"STATE_DIR", "")
	t.Setenv(EnvPrefix+"CONFIG_DIR", "")
	t.Setenv("XDG_STATE_HOME", "/x/state")
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	if got, want := StateDir(), filepath.Join("/x/state", Name); got != want {
		t.Errorf("state dir = %s, want %s", got, want)
	}
	if got, want := ConfigDir(), filepath.Join("/x/config", Name); got != want {
		t.Errorf("config dir = %s, want %s", got, want)
	}
}

// §3.5: the state dir is this user's alone, and a dir that already exists more
// widely is tightened rather than accepted.
func TestTheStateDirIsClosedToOtherUsers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvPrefix+"STATE_DIR", dir)
	if err := EnsureStateDir(); err != nil {
		t.Fatalf("EnsureStateDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("state dir is %o, want 0700", perm)
	}
}

// A relative state dir means nothing named the store, and creating one anyway
// would put a store under every working directory the binary is run from.
func TestARelativeStateDirIsRefusedRatherThanCreated(t *testing.T) {
	t.Setenv(EnvPrefix+"STATE_DIR", "relative/state")
	err := EnsureStateDir()
	if err == nil {
		t.Fatal("a relative state dir was accepted")
	}
	if codes.Of(err) != codes.Unavailable {
		t.Fatalf("code = %s, want UNAVAILABLE", codes.Of(err))
	}
}

// A missing config file is the unconfigured default, which §9.2 makes an
// allowing gate and nothing else.
func TestAMissingConfigIsTheUnconfiguredDefault(t *testing.T) {
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "sched.toml"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Present {
		t.Error("a file that does not exist was reported as present")
	}
	if cfg.TickSeconds != DefaultTickSeconds {
		t.Errorf("tick_seconds = %d, want the default %d", cfg.TickSeconds, DefaultTickSeconds)
	}
	if len(cfg.GateCommand) != 0 {
		t.Errorf("gate command = %v, want none", cfg.GateCommand)
	}
	// The shipped webhook default has to REACH a loaded config, not merely
	// exist as a constant beside it. A Load that spells its own address is a
	// second default nobody edits when the first one moves.
	if cfg.WebhookAddr != DefaultWebhookAddr {
		t.Errorf("webhook_addr = %q, want the default %q", cfg.WebhookAddr, DefaultWebhookAddr)
	}
}

func TestEveryDocumentedKeyIsRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.toml")
	write(t, path, `tick_seconds = 5
gate_command = ["/usr/local/bin/gate", "--quiet"]
on_event = ["/usr/local/bin/notify"]
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !cfg.Present || cfg.TickSeconds != 5 {
		t.Fatalf("config = %+v", cfg)
	}
	if strings.Join(cfg.GateCommand, " ") != "/usr/local/bin/gate --quiet" {
		t.Errorf("gate command = %v", cfg.GateCommand)
	}
	if strings.Join(cfg.OnEvent, " ") != "/usr/local/bin/notify" {
		t.Errorf("on_event = %v", cfg.OnEvent)
	}
}

// §9.2 fails closed: a gate command written as a bare string would assign
// nothing and read as unconfigured, which is every verb allowed by a typo.
func TestAScalarGateCommandIsRefusedRatherThanReadAsUnconfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.toml")
	write(t, path, "gate_command = \"/usr/local/bin/gate\"\n")
	if _, err := LoadFrom(path); err == nil {
		t.Fatal("a scalar gate_command was accepted")
	}
}

func TestAnUnknownKeyIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.toml")
	write(t, path, "sweep_seconds = 5\n")
	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "sweep_seconds") {
		t.Fatalf("the refusal does not name the key: %v", err)
	}
}

// An override is spelled SCHED_<KEY>, and it takes precedence over the file.
func TestAnEnvironmentOverrideBeatsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sched.toml")
	write(t, path, "tick_seconds = 5\n")
	t.Setenv(EnvPrefix+"TICK_SECONDS", "11")
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.TickSeconds != 11 {
		t.Fatalf("tick_seconds = %d, want the override's 11", cfg.TickSeconds)
	}
}

// doctor points at a store a build could have left elsewhere, and never at the
// one in use — pointing at live data is how an operator deletes a schedule.
func TestOrphanStoreDirsNeverNameTheStoreInUse(t *testing.T) {
	state := t.TempDir()
	t.Setenv(EnvPrefix+"STATE_DIR", state)
	t.Setenv("HERDR_PLUGIN_STATE_DIR", state)
	for _, dir := range OrphanStoreDirs() {
		if dir == state {
			t.Fatalf("the store in use, %s, is listed as an orphan", dir)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The shipped webhook default has to be a port the fleet running this plugin
// does not already own. proxenos has served 127.0.0.1:8787 on the reference
// machine since before this plugin existed, so shipping that port meant every
// daemon there started with its webhook door dead and doctor naming a
// collision nobody chose. The default is stable rather than ephemeral on
// purpose: a webhook URL that moves on every restart breaks every caller
// already configured, and it breaks them at the caller, where nobody here is
// watching.
func TestTheShippedWebhookDefaultIsLoopbackAndNotAPortTheFleetOwns(t *testing.T) {
	host, port, err := net.SplitHostPort(DefaultWebhookAddr)
	if err != nil {
		t.Fatalf("DefaultWebhookAddr %q is not host:port: %v", DefaultWebhookAddr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Errorf("DefaultWebhookAddr host = %q, want a loopback address: the trust boundary is the local user account", host)
	}
	if port == "0" {
		t.Error("DefaultWebhookAddr asks the kernel for any free port: a webhook URL that moves on every restart breaks every caller already configured")
	}
	// Ports on the reference fleet that another daemon owned first.
	for _, taken := range []struct{ port, owner string }{
		{"8787", "proxenos"},
	} {
		if port == taken.port {
			t.Errorf("DefaultWebhookAddr = %q, but %s serves %s on the reference machine: the door would be dead on arrival there",
				DefaultWebhookAddr, taken.owner, taken.port)
		}
	}
}
