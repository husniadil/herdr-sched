// Package version is what this binary calls itself and what contract it
// claims to satisfy. Both are facts about the code rather than settings, and
// they live in one place because more than one surface repeats them: the
// binary's own `version`, doctor's report, and the plugin manifest Herdr
// installs, which a test holds to the same string.
package version

// Version is this plugin's own version. The manifest's version matches it.
const Version = "0.2.2"

// Contract is the version of the Herdr plugin contract this binary satisfies.
// The contract itself is not vendored here: it lives in agamemnon
// (`docs/contract.md`), with identical copies in herdr-tasks, herdr-mail and
// herdr-dispatch, and every section number in this repository cites it (§13.4).
const Contract = "0.10.1"

// Plugin is the id Herdr knows this plugin by, and the name the MCP door
// registers under (§13.1).
const Plugin = "herdr-sched"
