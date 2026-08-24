package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/husniadil/herdr-sched/internal/config"
	"github.com/husniadil/herdr-sched/internal/daemon"
	"github.com/husniadil/herdr-sched/internal/fire"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/version"
)

// serve is the daemon: it owns the store and the tick, and answers the socket
// both doors dial.
func serve(f *daemonFlags) error {
	// The log is opened here, before anything worth reading is said. Where it
	// goes is the daemon's own call for the same reason the socket, the lock
	// and the store are: a shell line that redirects elsewhere can be lost on
	// a restart, and it is only ever missed once the log is already needed.
	if err := config.EnsureStateDir(); err != nil {
		return err
	}
	logOut, logErr := daemon.OpenLog(f.logPath, os.Stdout)
	if logOut != os.Stdout {
		defer logOut.Close()
	}
	log.SetOutput(logOut)
	if logErr != nil {
		f.logPath = ""
		log.Printf("%v; logging to stdout alone", logErr)
	}

	// §10.1: read once, at startup. The daemon holds the document it started
	// with, and a change to the file takes effect on the next start.
	cfg, err := config.LoadFrom(f.configPath)
	if err != nil {
		return err
	}

	// One daemon per user (§2.3). The lock is what makes one answer true
	// across every caller rather than per process.
	lock, err := daemon.Lock()
	if err != nil {
		return err
	}
	defer lock.Close()

	st, err := store.Open(config.StorePath())
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := daemon.Listen()
	if err != nil {
		return err
	}
	defer ln.Close()

	interval := f.interval
	if interval <= 0 {
		interval = time.Duration(cfg.TickSeconds) * time.Second
	}
	log.Printf("listening on %s, ticking every %s", config.SocketPath(), interval)
	if !cfg.Present {
		// Said out loud rather than left to be inferred: §10.1 fixes one
		// config path per plugin, and an operator editing a file somewhere
		// else would otherwise see no effect and no reason.
		log.Printf("no config file at %s: every default applies", cfg.Path)
	}
	if len(cfg.GateCommand) == 0 {
		log.Print("no policy gate configured: every verb is allowed (§9.2)")
	}
	for _, dir := range config.OrphanStoreDirs() {
		log.Printf("a store left by another build could be under %s; this daemon is not using it", dir)
	}

	runner := &fire.Runner{Store: st}
	d := &daemon.Daemon{
		Store:    st,
		Fire:     runner,
		Config:   cfg,
		Interval: interval,
		Version:  version.Version,
		Log:      log.Default(),
		LogPath:  f.logPath,
		Lock:     lock,
	}
	// Every run reaches the followers and the §8.3 hook the same way every
	// other event does.
	runner.Emit = d.Emitted
	err = d.Serve(ctx, ln)
	// A shell action is detached from the tick, so the ones still running
	// have their outcome to write. Waiting for them is what keeps a run off
	// the trail from being a run that never happened.
	runner.Wait()
	log.Print("stopping")
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
