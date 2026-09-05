package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/json/v2"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// Draft contains only non-secret suggestions. Trust and permissions are chosen
// by the human on the local form, never inferred from an agent's suggestions.
type Draft struct {
	Address string `json:"address,omitempty"`
	Name    string `json:"name,omitempty"`
}

type Progress struct {
	ID      string   `json:"setup_id"`
	Status  string   `json:"status"`
	URL     string   `json:"url,omitempty"`
	Message string   `json:"message"`
	Receipt *Receipt `json:"receipt,omitempty"`
}

type browserSession struct {
	mu       sync.Mutex
	submit   sync.Mutex
	draft    Draft
	expires  time.Time
	progress Progress
	patch    *SettingsPatch
	changes  []SettingChange
}

// Browser is process-owned, not tool-call-owned. Passwords travel directly from
// a human's loopback browser to the enrollment service and OS credential store.
type Browser struct {
	service   *Service
	server    *http.Server
	address   string
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	sessions  map[string]*browserSession
	closed    bool
	closeOnce sync.Once
	closeErr  error
	handlers  sync.WaitGroup
}

func NewBrowser(service *Service) (*Browser, error) {
	if service == nil {
		return nil, ErrInvalid
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Browser{service: service, address: listener.Addr().String(), cancel: cancel, done: make(chan struct{}), sessions: make(map[string]*browserSession)}
	protection := new(http.CrossOriginProtection)
	b.server = &http.Server{Handler: protection.Handler(http.HandlerFunc(b.serve)), BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { defer close(b.done); _ = b.server.Serve(listener) }()
	return b, nil
}

func (b *Browser) Begin(draft Draft) (Progress, error) {
	if draft.Address != "" {
		origin, err := NormalizeAddress(draft.Address)
		if err != nil {
			return Progress{}, err
		}
		draft.Address = origin
	}
	if len(draft.Name) > 80 {
		return Progress{}, ErrInvalid
	}
	return b.begin(&browserSession{draft: draft})
}

func (b *Browser) Settings() (Settings, error) { return b.service.Settings() }

func (b *Browser) BeginUpdate(patch SettingsPatch) (Progress, error) {
	// Own a deep copy so approval always binds the exact displayed patch.
	encoded, err := json.Marshal(patch)
	if err != nil {
		return Progress{}, ErrInvalid
	}
	var owned SettingsPatch
	if err := json.Unmarshal(encoded, &owned); err != nil {
		return Progress{}, ErrInvalid
	}
	before, after, err := b.service.Preview(owned)
	if err != nil {
		return Progress{}, err
	}
	return b.begin(&browserSession{patch: &owned, changes: SettingChanges(before, after)})
}

func (b *Browser) begin(session *browserSession) (Progress, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Progress{}, errors.New("device setup is closed")
	}
	for id, session := range b.sessions {
		if time.Now().After(session.expires) {
			delete(b.sessions, id)
		}
	}
	if len(b.sessions) >= 8 {
		return Progress{}, errors.New("finish an existing setup or wait for it to expire")
	}
	id := rand.Text()
	p := Progress{ID: id, Status: "requires_user_action", URL: "http://" + b.address + "/setup/" + id, Message: "Open this link on the computer running JetKVM. Review the device and permissions; enter any password there, never in chat. Then check setup status."}
	if session.patch != nil {
		p.Message = "Open this local link to review and approve the exact configuration changes. Then check setup status; no MCP restart is needed."
	}
	session.expires, session.progress = time.Now().Add(10*time.Minute), p
	b.sessions[id] = session
	return p, nil
}

func (b *Browser) session(id string) (*browserSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	if b.closed || s == nil || time.Now().After(s.expires) {
		return nil, errors.New("setup expired or was not found; start a new setup")
	}
	return s, nil
}

func (b *Browser) Status(id string) (Progress, error) {
	s, err := b.session(id)
	if err != nil {
		return Progress{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress, nil
}

func (b *Browser) Wait(ctx context.Context, id string) (Receipt, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		p, err := b.Status(id)
		if err != nil {
			return Receipt{}, err
		}
		if p.Receipt != nil {
			return *p.Receipt, nil
		}
		select {
		case <-ctx.Done():
			return Receipt{}, context.Cause(ctx)
		case <-ticker.C:
		}
	}
}

func (b *Browser) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		b.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		b.closeErr = b.server.Shutdown(ctx)
		if b.closeErr != nil {
			b.closeErr = errors.Join(b.closeErr, b.server.Close())
		}
		<-b.done
		b.handlers.Wait()
	})
	return b.closeErr
}

func (b *Browser) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		http.Error(w, "Setup is closed", http.StatusServiceUnavailable)
		return
	}
	b.handlers.Add(1)
	b.mu.Unlock()
	defer b.handlers.Done()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || !remote.Addr().IsLoopback() || r.Host != b.address {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if r.Method != "GET" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "/setup/"
	if len(r.URL.Path) <= len(prefix) || r.URL.Path[:len(prefix)] != prefix || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	id := r.URL.Path[len(prefix):]
	s, err := b.session(id)
	if err != nil {
		http.Error(w, "Setup expired. Ask your agent to start again.", http.StatusGone)
		return
	}
	s.submit.Lock()
	defer s.submit.Unlock()
	if s.patch != nil {
		b.serveUpdate(w, r, s)
		return
	}
	s.mu.Lock()
	done := s.progress.Receipt != nil
	s.mu.Unlock()
	message := ""
	if r.Method == "POST" && !done {
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", http.StatusBadRequest)
			return
		}
		password := []byte(r.PostForm.Get("password"))
		r.PostForm.Del("password")
		r.Form.Del("password")
		defer clear(password)
		request := Request{Address: r.PostForm.Get("address"), Name: r.PostForm.Get("name"), AllowHTTP: r.PostForm.Get("trusted_http") == "yes", Control: r.PostForm.Get("control") == "yes"}
		if r.PostForm.Get("approve") != "yes" {
			message = "Review the device and approve this connection to continue."
		} else {
			receipt, err := b.service.Connect(r.Context(), request, Secret{Password: password})
			if err != nil {
				message = PublicMessage(err)
			} else {
				s.mu.Lock()
				s.progress.Status = "connected"
				s.progress.Message = "Device connected. Return to your agent or terminal."
				s.progress.URL = ""
				s.progress.Receipt = &receipt
				s.mu.Unlock()
				done = true
			}
		}
		s.draft = Draft{Address: request.Address, Name: request.Name}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = setupPage.Execute(w, struct {
		Draft   Draft
		Message string
		Done    bool
	}{s.draft, message, done})
}

func PublicMessage(err error) string {
	for _, safe := range []error{ErrPasswordRequired, ErrAuthentication, ErrConflict, ErrCredentialStore, ErrInvalid, ErrUnavailable, ErrActiveControls, ErrActivation, ErrRevisionConflict, ErrPolicyDenied} {
		if errors.Is(err, safe) {
			return safe.Error()
		}
	}
	if errors.Is(err, context.Canceled) {
		return "Setup was canceled."
	}
	return "Device setup could not complete. Check the address, trust choice, network, and credential store, then try again."
}

var setupPage = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Connect your JetKVM</title>
<style>body{font:17px system-ui;color:#e8edf4;background:#111820;margin:0;padding:32px 20px}main{max-width:480px;margin:5vh auto}h1{font-size:30px}p{line-height:1.5;color:#aab8c8}label{display:block;margin:20px 0 8px}input:not([type=checkbox]){box-sizing:border-box;width:100%;padding:12px;background:#1c2835;border:1px solid #56677c;border-radius:7px;color:inherit;font:inherit}button{margin-top:24px;padding:13px 22px;background:#83c9f4;color:#102333;border:0;border-radius:7px;font:inherit;font-weight:600}a{color:#83c9f4}.error{color:#ffb4a9}small{color:#aab8c8}</style>
<main>{{if .Done}}<h1>JetKVM is connected</h1><p>Return to your agent or terminal. You can now inspect the device and capture its screen. Connecting did not open a control session or send any input.</p>{{else}}<h1>Connect your JetKVM</h1><p>Enter the address shown on your JetKVM. Device identity and local settings are handled for you.</p>
{{if .Message}}<p class="error" role="alert">{{.Message}}</p>{{end}}
<form method="post" autocomplete="off"><label for="address">Device address</label><input id="address" name="address" value="{{.Draft.Address}}" placeholder="jetkvm.local" required maxlength="2048" autofocus>
<label for="name">Name <small>(optional)</small></label><input id="name" name="name" value="{{.Draft.Name}}" placeholder="My computer" maxlength="80">
<label for="password">Device password <small>(only if configured)</small></label><input id="password" type="password" name="password" maxlength="4096" autocomplete="current-password"><small>Saved in your operating system's credential store, never sent to your agent.</small>
<label><input type="checkbox" name="trusted_http" value="yes"> This device is on my trusted network; allow its unencrypted HTTP connection.</label>
<label><input type="checkbox" name="control" value="yes"> Also allow keyboard and mouse control.</label><small>Screen viewing is included. Power actions are not enabled. Taking over a session still requires confirmation.</small>
<label><input type="checkbox" name="approve" value="yes" required> Connect this device with these permissions.</label><button type="submit">Connect JetKVM</button><p>You can close this page to cancel. This link expires in 10 minutes.</p></form>{{end}}</main></html>`))
