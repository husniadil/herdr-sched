// Command hsched is the scheduler and trigger plugin for a Herdr fleet: a
// cron schedule or an inbound trigger fires an action into the sibling
// plugins, as the principal the contract already names for it.
//
// One binary is the daemon and both doors. `hsched daemon` owns the store and
// the tick; `hsched <verb>` and `hsched mcp` are thin clients of it, and start
// one when none is running.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/husniadil/herdr-sched/internal/cli"
	"github.com/husniadil/herdr-sched/internal/codes"
)

func main() {
	log.SetFlags(log.LstdFlags)
	log.SetPrefix("hsched: ")

	// --json has to be known BEFORE cobra parses, because cobra's own parse
	// failures are among the failures that must answer with one document
	// (§6.2). At that moment the flag exists only in argv.
	asJSON := cli.WantsJSON(os.Args[1:])

	err := newRootCmd().Execute()
	if err == nil {
		return
	}
	// One failure, one report, one stream (§6.2): with --json the envelope IS
	// the report and it goes to stdout, otherwise a sentence goes to stderr
	// and stdout stays empty. The status is the one §6.3 fixes for the code,
	// so a caller scripting the sibling plugins reads the same number from
	// each. Cobra's own failures — an unknown flag, an unknown subcommand —
	// are caller input errors, which the contract fixes at USAGE, and codes.Of
	// gives an unnamed error UNAVAILABLE, so they are named here rather than
	// left to fall through.
	err = cli.AsRefusal(err)
	code := codes.Of(err)
	if asJSON {
		cli.WriteError(err, os.Stdout)
	} else {
		fmt.Fprintf(os.Stderr, "hsched: %s\n", err)
	}
	os.Exit(codes.Exit(code))
}
