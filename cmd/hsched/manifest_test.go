package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/verbs"
	"github.com/husniadil/herdr-sched/internal/version"
)

// repoFile reads a file from the repository root, which is two directories up
// from this package.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// The manifest's version is this binary's version. Two places that must agree
// and nothing checking them is how an operator installs 0.1.0 and runs 0.2.0.
func TestTheManifestVersionIsTheBinarysVersion(t *testing.T) {
	manifest := repoFile(t, "herdr-plugin.toml")
	if !strings.Contains(manifest, `version = "`+version.Version+`"`) {
		t.Fatalf("the manifest does not declare version %q", version.Version)
	}
	if !strings.Contains(manifest, `id = "`+version.Plugin+`"`) {
		t.Fatalf("the manifest does not declare id %q", version.Plugin)
	}
}

// §13.1: the plugin id, the binary and the short name are three names, and the
// manifest builds the binary this package actually is.
func TestTheManifestBuildsThisBinary(t *testing.T) {
	manifest := repoFile(t, "herdr-plugin.toml")
	if !strings.Contains(manifest, `"./bin/hsched", "./cmd/hsched"`) {
		t.Fatal("the [[build]] step does not build ./cmd/hsched into ./bin/hsched")
	}
}

// Every script the manifest names exists and is executable. A manifest that
// names a script Herdr cannot run fails at install, on the operator's machine,
// with no line here to have caught it.
func TestEveryScriptTheManifestNamesIsThereAndExecutable(t *testing.T) {
	manifest := repoFile(t, "herdr-plugin.toml")
	named := []string{}
	for _, line := range strings.Split(manifest, "\n") {
		if !strings.Contains(line, "./scripts/") {
			continue
		}
		_, rest, _ := strings.Cut(line, "./scripts/")
		name, _, _ := strings.Cut(rest, `"`)
		named = append(named, name)
	}
	if len(named) < 4 {
		t.Fatalf("the manifest names %d scripts; the standard's set is start, stop, restart and on-pane-gone", len(named))
	}
	for _, name := range named {
		info, err := os.Stat(filepath.Join("..", "..", "scripts", name))
		if err != nil {
			t.Errorf("the manifest names scripts/%s and it is not there: %v", name, err)
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("scripts/%s is not executable", name)
		}
	}
}

// §9.4: the README carries the gated verb list a policy plugin names, and a
// test reads one against the other — a gate name only the code knows is a
// policy an operator cannot write.
func TestTheREADMECarriesTheGatedVerbs(t *testing.T) {
	readme := repoFile(t, "README.md")
	for _, gated := range verbs.GatedVerbs() {
		if !strings.Contains(readme, gated) {
			t.Errorf("the README does not name the gated verb %q", gated)
		}
	}
}

// The README's MCP section is the list an operator wires a client against, and
// a tool the door serves and the README does not name is a verb nobody knows
// is there.
func TestTheREADMENamesEveryMCPTool(t *testing.T) {
	readme := repoFile(t, "README.md")
	for _, v := range verbs.MCPTools() {
		if !strings.Contains(readme, "`"+v.MCP+"`") {
			t.Errorf("the README does not name the MCP tool %q", v.MCP)
		}
	}
}

// §10.1 fixes one config path per plugin. The README's Configuration section
// is keyed exactly as the TOML document spells it, so a key the code reads and
// the README does not name is a knob nobody can find.
func TestTheREADMEDocumentsEveryConfigKey(t *testing.T) {
	readme := repoFile(t, "README.md")
	for _, key := range config.Keys {
		if !strings.Contains(readme, key) {
			t.Errorf("the README does not document the config key %q", key)
		}
	}
	if !strings.Contains(readme, config.ConfigPath()) && !strings.Contains(readme, "sched/sched.toml") {
		t.Error("the README does not name the one config path")
	}
}

// The README answers the four questions the repo standard makes shared, in the
// places it makes them shared.
func TestTheREADMEIsInTheStandardShape(t *testing.T) {
	readme := repoFile(t, "README.md")
	install := strings.Index(readme, "\n## Install")
	if install < 0 {
		t.Fatal("there is no `## Install` heading, spelled exactly that")
	}
	// It is the FIRST heading, and a paragraph says what the plugin is before
	// it.
	if first := strings.Index(readme, "\n## "); first != install {
		t.Errorf("`## Install` is not the first heading; %q is", strings.SplitN(readme[first+4:], "\n", 2)[0])
	}
	if strings.TrimSpace(readme[:install]) == "" {
		t.Error("nothing says what the plugin is before the first heading")
	}
	config := strings.Index(readme, "\n## Configuration")
	if config < 0 {
		t.Fatal("there is no `## Configuration` heading, spelled exactly that")
	}
	// How the verbs are reached on both doors comes BEFORE Configuration.
	doors := strings.Index(readme, "hsched mcp")
	if doors < 0 || doors > config {
		t.Error("how the verbs are reached on both doors is not answered before Configuration")
	}
	for what, want := range map[string]string{
		"the install command":  "herdr plugin install husniadil/herdr-sched",
		"the skill symlink":    "skills/sched",
		"the checkout variant": "herdr plugin link .",
		"the licence":          "MIT",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("the README does not carry %s (looked for %q)", what, want)
		}
	}
}

// The CHANGELOG names the version this binary is.
func TestTheChangelogNamesThisVersion(t *testing.T) {
	if !strings.Contains(repoFile(t, "CHANGELOG.md"), version.Version) {
		t.Fatalf("the CHANGELOG does not mention %s", version.Version)
	}
}

// The dashboard's pane is in the manifest, and it is a POPUP. A script the
// repo carries and the manifest does not open is a view no operator can
// reach, and the manifest is the only place that wiring exists.
func TestTheManifestOpensTheDashboardInAPopup(t *testing.T) {
	manifest := repoFile(t, "herdr-plugin.toml")
	pane := strings.Index(manifest, "[[panes]]")
	if pane < 0 {
		t.Fatal("the manifest declares no [[panes]] entry, so the dashboard opens nowhere")
	}
	entry := manifest[pane:]
	if next := strings.Index(entry[len("[[panes]]"):], "\n[["); next >= 0 {
		entry = entry[:len("[[panes]]")+next]
	}
	for what, want := range map[string]string{
		"a popup placement":    `placement = "popup"`,
		"the dashboard script": `"./scripts/popup-dashboard.sh"`,
	} {
		if !strings.Contains(entry, want) {
			t.Errorf("the [[panes]] entry does not carry %s (looked for %q)", what, want)
		}
	}
}

// A divergence the notes still record and the manifest has closed is worse
// than one that was never written down: it is a reader being told the opposite
// of what the plugin does. The pane entry is the one this repo actually closed,
// and this is what stops it reopening on paper.
func TestTheNotesDoNotStillDenyThePane(t *testing.T) {
	manifest := repoFile(t, "herdr-plugin.toml")
	notes := repoFile(t, filepath.Join("docs", "contract-notes.md"))
	if !strings.Contains(manifest, "[[panes]]") {
		return
	}
	if strings.Contains(notes, "no `[[panes]]`") {
		t.Error("docs/contract-notes.md still records `no [[panes]]` as a divergence, and the manifest declares one")
	}
}

// The dashboard READS. A popup carries no HERDR_PANE_ID, so its principal is
// the human who opened it, and a mutating call from there is one walking past
// the §9 gate that every write is held behind. The check is against the
// registry rather than a list written here, so a verb that starts mutating
// is covered the day it does.
func TestTheDashboardCallsNoVerbThatWrites(t *testing.T) {
	script := repoFile(t, filepath.Join("scripts", "popup-dashboard.sh"))
	const marker = `"$HSCHED" `
	found := 0
	for rest := script; ; {
		at := strings.Index(rest, marker)
		if at < 0 {
			break
		}
		rest = rest[at+len(marker):]
		fields := strings.Fields(rest)
		verb, ok := longestVerb(fields)
		if !ok {
			t.Errorf("the dashboard invokes something the verb registry does not name: %q", firstN(fields, 2))
			continue
		}
		found++
		if verb.Mutates {
			t.Errorf("the dashboard calls %q, which writes: the popup is read-only", verb.Name)
		}
	}
	if found == 0 {
		t.Fatal("no invocation was found at all; the marker this test scans for has moved")
	}
}

// longestVerb resolves the longest CLI path the leading fields spell, so
// `job list` is read as the two-word verb rather than as `job`.
func longestVerb(fields []string) (verbs.Verb, bool) {
	for n := 2; n >= 1; n-- {
		if len(fields) < n {
			continue
		}
		if v, ok := verbs.ByCLI(fields[:n]); ok {
			return v, true
		}
	}
	return verbs.Verb{}, false
}

func firstN(fields []string, n int) []string {
	if len(fields) < n {
		return fields
	}
	return fields[:n]
}

// The dashboard never reaches for the secrets file. `trigger list` carries no
// webhook secret because the key is not in the document it reads, and this is
// what keeps that true of the popup as well: the rule is carried by where the
// key IS, and a script that opened the file would walk straight around it.
func TestTheDashboardNeverReadsTheSecretsFile(t *testing.T) {
	script := repoFile(t, filepath.Join("scripts", "popup-dashboard.sh"))
	name := filepath.Base(config.SecretsPath())
	for _, line := range strings.Split(script, "\n") {
		if !strings.Contains(line, name) {
			continue
		}
		// Saying WHY it is not read is the one mention that is allowed.
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		t.Errorf("the dashboard names %s outside a comment: %q", name, strings.TrimSpace(line))
	}
}

// jq is the dashboard's own runtime requirement, and a machine without it
// gets a loud refusal rather than an empty dashboard. It is not a dependency
// of this plugin: nothing Go builds needs it.
func TestTheDashboardRefusesWithoutJQ(t *testing.T) {
	script := repoFile(t, filepath.Join("scripts", "popup-dashboard.sh"))
	if !strings.Contains(script, "command -v jq") {
		t.Error("the dashboard does not check for jq, so a machine without it draws an empty view")
	}
}
