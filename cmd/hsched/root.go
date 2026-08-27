package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/husniadil/herdr-sched/internal/cli"
	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/mcpdoor"
	"github.com/husniadil/herdr-sched/internal/version"
)

// newRootCmd is the whole command tree: the verbs come from the registry both
// doors are generated from, and the three commands that are not verbs — the
// daemon, the MCP door and the version — are added here beside them.
func newRootCmd() *cobra.Command {
	root := cli.Root(cli.Send)
	root.AddCommand(newDaemonCmd(), newMCPCmd(), newVersionCmd())
	return root
}

// daemonFlags are the daemon's own knobs. They stay flags on this subcommand
// rather than globals: they configure the process that owns the store, and
// mean nothing to a client call.
type daemonFlags struct {
	configPath string
	logPath    string
	interval   time.Duration
}

func newDaemonCmd() *cobra.Command {
	f := &daemonFlags{}
	cmd := &cobra.Command{
		Use:     "daemon",
		Aliases: []string{"run"},
		Short:   "Own the store and the tick, and answer both doors",
		Long: "The daemon owns the store and the tick, and answers the socket both " +
			"doors dial. `hsched run` is the same command. It refuses when another " +
			"daemon already holds the lock (§2.3).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return serve(f) },
	}
	fs := cmd.Flags()
	fs.StringVar(&f.configPath, "config", config.ConfigPath(), "the TOML config read once at startup")
	fs.StringVar(&f.logPath, "log", config.LogPath(), "the file the log is appended to")
	fs.DurationVar(&f.interval, "interval", 0, `how often to tick; 0 means the config's "tick_seconds"`)
	return cmd
}

func newMCPCmd() *cobra.Command {
	var opt mcpdoor.Options
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the same verbs over stdio MCP",
		Long: "A thin door over the same daemon calls the CLI makes (§7.2). Both\n" +
			"surfaces are first-class and the door serves every verb the CLI serves\n" +
			"(§7.3). What a door cannot have is `--as`, which is an identity claim\n" +
			"carried BY a call; --operator is its counterpart and travels with this\n" +
			"process instead (§7.5).",
		// The door takes no positional arguments, and silently ignoring one is
		// how a caller ends up believing it passed something that took effect.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcpdoor.Serve(context.Background(), version.Version, nil, opt)
		},
	}
	// §7.5: read once, from the server command. It is deliberately NOT a
	// persistent flag — a flag every verb carried would be a per-call
	// declaration, which is the thing this exists instead of.
	cmd.Flags().BoolVar(&opt.Operator, "operator", false,
		"Declare that this door speaks for the operator (§7.5). Set it once, in the client's\n"+
			"server configuration, where a human wrote it deliberately. Without it a door in no\n"+
			"Herdr pane is nobody, because absence of evidence is not evidence of the operator.")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the plugin version and the contract it satisfies",
		// The contract revision travels with the version, the way both
		// siblings print it: §13.4 makes a plugin declare it, and a reader
		// comparing three binaries should not have to run a different verb on
		// one of them to see it. `--json` answers the same three facts.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if on, _ := cmd.Flags().GetBool("json"); on {
				out, _ := json.Marshal(map[string]string{
					"version": version.Version, "contract": version.Contract, "plugin": version.Plugin,
				})
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "hsched %s (%s), shared plugin contract %s\n",
				version.Version, version.Plugin, version.Contract)
			return nil
		},
	}
}
