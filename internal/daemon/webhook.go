package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/husniadil/herdr-sched/internal/codes"
	"github.com/husniadil/herdr-sched/internal/store"
	"github.com/husniadil/herdr-sched/internal/trigger"
)

// WebhookPrefix is the one path this door answers on. Everything under it is a
// trigger id, and nothing else on the server is anything at all.
const WebhookPrefix = "/trigger/"

// MaxBody is how much of an inbound request this door will read. A signature
// is computed over the WHOLE body, so a body with no ceiling is a stranger
// choosing how much memory this daemon spends before it has proved anything.
const MaxBody = 1 << 20

// WebhookOff is the address that means no inbound door at all, which is what a
// fleet with only file watchers wants: a port nothing uses is a port worth not
// opening.
const WebhookOff = "off"

// webhookTimeouts bound a connection that never finishes saying anything.
const (
	webhookReadTimeout  = 15 * time.Second
	webhookWriteTimeout = 15 * time.Second
	webhookIdleTimeout  = 60 * time.Second
)

// inbound is the webhook door as doctor reports it: where it is listening, or
// why it is not.
type inbound struct {
	mu   sync.Mutex
	addr string
	err  string
}

func (i *inbound) set(addr, err string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.addr, i.err = addr, err
}

func (i *inbound) read() (string, string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.addr, i.err
}

// listenWebhooks opens the inbound door, or says why it could not.
//
// A door that cannot bind is NEVER a reason not to start: the cron half and
// every file watcher still work, doctor names the failure, and the operator
// fixes the port. Failing to start the whole daemon over one port would take
// every schedule down with it.
func (d *Daemon) listenWebhooks() net.Listener {
	addr := strings.TrimSpace(d.WebhookAddr)
	if addr == "" || addr == WebhookOff {
		d.inbound.set("", "")
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		reason := codes.Message(codes.Errorf(codes.Unavailable, "listen for webhooks on %s: %v", addr, err))
		d.inbound.set("", reason)
		d.logf("%s; the cron half and every file watcher are unaffected", reason)
		return nil
	}
	d.inbound.set(ln.Addr().String(), "")
	d.logf("webhook triggers are reachable at http://%s%s<id>", ln.Addr().String(), WebhookPrefix)
	return ln
}

// serveWebhooks answers the inbound door until ctx ends.
func (d *Daemon) serveWebhooks(ctx context.Context, ln net.Listener) {
	if ln == nil {
		return
	}
	srv := &http.Server{
		Handler:      http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { d.webhook(ctx, w, r) }),
		ReadTimeout:  webhookReadTimeout,
		WriteTimeout: webhookWriteTimeout,
		IdleTimeout:  webhookIdleTimeout,
	}
	go func() {
		<-ctx.Done()
		// The connections in flight are given the same grace the daemon's own
		// shutdown takes; a request already verified is one this daemon owes
		// an answer to.
		shutdown, cancel := context.WithTimeout(context.Background(), webhookWriteTimeout)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		d.logf("the webhook door stopped: %v", err)
	}
	d.inbound.set("", "")
}

// webhookURL is where one trigger is reached, empty when no door is listening.
func (d *Daemon) webhookURL(id string) string {
	addr, _ := d.inbound.read()
	if addr == "" {
		return ""
	}
	return "http://" + addr + WebhookPrefix + id
}

// webhook is the whole of what an inbound request meets.
//
// The order is the point. The body is read raw and the signature is verified
// over it BEFORE anything parses a byte a stranger sent — this door never
// unmarshals an unverified payload, and it never needs to: what the body says
// is not what decides anything, the trigger id in the path and the signature
// over the bytes are.
func (d *Daemon) webhook(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		refuse(w, http.StatusMethodNotAllowed, codes.Usage,
			"a webhook trigger is fired with POST, and this was "+r.Method)
		return
	}
	id, found := strings.CutPrefix(r.URL.Path, WebhookPrefix)
	if !found || id == "" || strings.Contains(id, "/") {
		refuse(w, http.StatusNotFound, codes.NotFound,
			"a webhook trigger is reached at "+WebhookPrefix+"<id>")
		return
	}
	t, held := d.Store.Trigger(id)
	if !held || t.Kind != trigger.KindWebhook {
		// Nothing to name on the trail: an id no row answers to is not a
		// trigger that was dropped, it is a request for something that does
		// not exist. Both answers read the same from outside, which is what
		// keeps this door from being a way to enumerate the ids there are.
		refuse(w, http.StatusNotFound, codes.NotFound, "no webhook trigger called "+id)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		d.dropped(t, "the body could not be read whole: "+err.Error())
		refuse(w, http.StatusRequestEntityTooLarge, codes.Usage,
			"the body is longer than this door reads")
		return
	}
	secret, hasSecret := d.secretFor(id)
	if !hasSecret {
		d.dropped(t, "this daemon holds no secret for the trigger, so nothing could be verified")
		refuse(w, http.StatusForbidden, codes.Forbidden,
			"this daemon holds no secret for "+id+"; remove the trigger and write it again for a key it knows")
		return
	}
	if err := trigger.Verify(secret, body, r.Header.Get(trigger.SignatureHeader)); err != nil {
		// An unverified request is DROPPED with a trail event naming the
		// trigger, and fires nothing (note 2). It is on the trail rather than
		// swallowed: a URL being probed is something an operator wants to see.
		d.dropped(t, codes.Message(err))
		refuse(w, http.StatusForbidden, codes.Forbidden, codes.Message(err))
		return
	}
	// Verified, and only now does the row's own decision get asked. A refusal
	// from here is a genuine signal held down rather than a forgery dropped.
	verdict := d.fireTrigger(ctx, t, map[string]any{
		"trigger_kind": trigger.KindWebhook, "bytes": len(body),
	})
	if !verdict.Fire {
		answer(w, http.StatusTooManyRequests, map[string]any{
			"trigger": id, "fired": false, "limit": verdict.Limit, "reason": verdict.Reason,
		})
		return
	}
	answer(w, http.StatusAccepted, map[string]any{
		"trigger": id, "fired": true, "action": t.Action.Kind,
	})
}

// secretFor is one trigger's key, and the only place anything reads one.
func (d *Daemon) secretFor(id string) (string, bool) {
	if d.Secrets == nil {
		return "", false
	}
	return d.Secrets.Get(id)
}

// dropped records an inbound request that was refused before it could fire,
// naming the trigger it was aimed at.
func (d *Daemon) dropped(t trigger.Trigger, reason string) {
	d.recordTriggerRun(t, store.KindDropped, map[string]any{
		"trigger_kind": trigger.KindWebhook, "action": t.Action.Kind, "error": reason,
	})
	d.logf("an inbound request for the trigger %s was dropped: %s", t.ID, reason)
}

// refuse answers one inbound request in the §6.3 envelope the doors already
// use, so an operator reading a curl output reads the same shape they read
// everywhere else.
func refuse(w http.ResponseWriter, status int, code codes.Code, message string) {
	answer(w, status, map[string]any{"error": map[string]string{
		"code": string(code), "message": message,
	}})
}

func answer(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
