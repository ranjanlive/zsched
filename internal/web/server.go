package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
	"zsched/internal/assistant"
	"zsched/internal/platform"
	"zsched/internal/scheduler"
)

//go:embed assets
var assets embed.FS

type Endpoint struct {
	Addr   string `json:"addr"`
	Token  string `json:"token"`
	PID    int    `json:"pid"`
	WebURL string `json:"web_url,omitempty"`
}
type Server struct {
	mu            sync.Mutex
	s             *scheduler.Service
	AI            *assistant.Assistant
	token         string
	control       *http.Server
	controlAddr   string
	dashboard     *http.Server
	dashboardAddr string
	Shutdown      func()
	exe           string
}

func New(s *scheduler.Service) (*Server, error) {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return nil, e
	}
	exe, _ := os.Executable()
	v := &Server{s: s, token: hex.EncodeToString(b[:]), exe: exe}
	v.AI = assistant.New(s)
	v.AI.Action = v.Action
	v.AI.ActionContext = v.ActionContext
	s.ExecuteBuiltin = func(ctx context.Context, r scheduler.Run) (string, error) {
		a := assistant.New(s)
		a.Action = v.Action
		a.ActionContext = v.ActionContext
		a.HistoryPath = filepath.Join(s.Home(), "assistant-sessions", r.SessionID+".json")
		if b, e := os.ReadFile(a.HistoryPath); e == nil {
			if e = a.Load(b); e != nil {
				return "", e
			}
		}
		result, e := a.Chat(ctx, r.Task)
		return result.Reply, e
	}
	if b, e := os.ReadFile(filepath.Join(s.Home(), "assistant-history.json")); e == nil {
		if e = v.AI.Load(b); e != nil {
			return nil, fmt.Errorf("assistant history is damaged: %w", e)
		}
	}
	return v, nil
}
func (v *Server) Start() error {
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	v.controlAddr = ln.Addr().String()
	v.control = &http.Server{Handler: v.handler(false), ReadHeaderTimeout: 10 * time.Second}
	go v.control.Serve(ln)
	if e = v.publish(); e != nil {
		return e
	}
	if v.s.Snapshot().Settings.WebEnabled {
		return v.StartWeb()
	}
	return nil
}
func (v *Server) publish() error {
	b, _ := json.Marshal(Endpoint{v.controlAddr, v.token, os.Getpid(), v.webURLLocked()})
	return scheduler.Atomic(filepath.Join(v.s.Home(), "daemon.json"), b)
}
func (v *Server) webURLLocked() string {
	if v.dashboardAddr == "" {
		return ""
	}
	return "http://" + v.dashboardAddr + "/#token=" + v.token
}
func (v *Server) WebURL() string { v.mu.Lock(); defer v.mu.Unlock(); return v.webURLLocked() }
func (v *Server) StartWeb() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.dashboard != nil {
		return nil
	}
	port := v.s.Snapshot().Settings.WebPort
	ln, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if e != nil {
		return fmt.Errorf("cannot start web interface on port %d: %w", port, e)
	}
	v.dashboardAddr = ln.Addr().String()
	v.dashboard = &http.Server{Handler: v.handler(true), ReadHeaderTimeout: 10 * time.Second}
	go v.dashboard.Serve(ln)
	cfg := v.s.Snapshot().Settings
	cfg.WebEnabled = true
	if e = v.s.SetSettings(cfg); e != nil {
		v.dashboard.Close()
		v.dashboard = nil
		v.dashboardAddr = ""
		return e
	}
	return v.publish()
}
func (v *Server) StopWeb() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.dashboard != nil {
		server := v.dashboard
		go func() {
			ctx, c := context.WithTimeout(context.Background(), 2*time.Second)
			defer c()
			server.Shutdown(ctx)
		}()
		v.dashboard = nil
		v.dashboardAddr = ""
	}
	cfg := v.s.Snapshot().Settings
	cfg.WebEnabled = false
	if e := v.s.SetSettings(cfg); e != nil {
		return e
	}
	return v.publish()
}
func (v *Server) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.dashboard != nil {
		v.dashboard.Close()
	}
	if v.control != nil {
		v.control.Close()
	}
	os.Remove(filepath.Join(v.s.Home(), "daemon.json"))
}
func (v *Server) Open() error {
	if e := v.StartWeb(); e != nil {
		return e
	}
	return platform.OpenURL(v.WebURL())
}
func respond(w http.ResponseWriter, status int, x any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(x)
}
func fail(w http.ResponseWriter, e error) { respond(w, 400, map[string]string{"error": e.Error()}) }
func decode(w http.ResponseWriter, r *http.Request, x any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(x); e != nil {
		return e
	}
	var rest any
	if e := d.Decode(&rest); e != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
func (v *Server) handler(ui bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{"state": v.s.Snapshot(), "version": scheduler.Version, "web_url": v.WebURL(), "health": v.s.Health()})
	})
	mux.HandleFunc("POST /api/action", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Operation string          `json:"operation"`
			Payload   json.RawMessage `json:"payload"`
		}
		if e := decode(w, r, &q); e != nil {
			fail(w, e)
			return
		}
		x, e := v.Action(q.Operation, q.Payload)
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, map[string]any{"result": x})
	})
	mux.HandleFunc("GET /api/log/{id}", func(w http.ResponseWriter, r *http.Request) {
		x, e := v.s.RunLog(r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, map[string]string{"output": x})
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Text string `json:"text"`
		}
		if e := decode(w, r, &q); e != nil {
			fail(w, e)
			return
		}
		x, e := v.AI.Chat(r.Context(), q.Text)
		if e != nil {
			fail(w, e)
			return
		}
		respond(w, 200, x)
	})
	mux.HandleFunc("GET /api/chat", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, v.AI.History()) })
	if ui {
		sub, _ := fs.Sub(assets, "assets")
		mux.Handle("GET /", http.FileServerFS(sub))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, p, e := net.SplitHostPort(r.Host)
		if e != nil || (h != "127.0.0.1" && h != "localhost") {
			http.Error(w, "Forbidden host", 403)
			return
		}
		local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if local != nil {
			_, actual, _ := net.SplitHostPort(local.String())
			if p != actual {
				http.Error(w, "Forbidden host", 403)
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || u.Scheme != "http" {
				http.Error(w, "Forbidden origin", 403)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			tok := r.Header.Get("Authorization")
			if subtle.ConstantTimeCompare([]byte(tok), []byte("Bearer "+v.token)) != 1 {
				respond(w, 401, map[string]string{"error": "Open the dashboard with its access token or use the local CLI"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (v *Server) ActionContext(ctx context.Context, op string, raw json.RawMessage) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Scheduler mutations are atomic. Once one begins it commits or rolls back;
	// cancellation prevents subsequent mutations from starting.
	return v.Action(op, raw)
}

func (v *Server) Action(op string, raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	var q struct {
		ID string `json:"id"`
		UI string `json:"ui"`
	}
	json.Unmarshal(raw, &q)
	switch op {
	case "state":
		return v.s.Snapshot(), nil
	case "agent_save":
		var a scheduler.Agent
		if e := json.Unmarshal(raw, &a); e != nil {
			return nil, e
		}
		return v.s.SaveAgent(a)
	case "agent_delete":
		return nil, v.s.DeleteAgent(q.ID)
	case "agent_open":
		if q.UI == "" {
			q.UI = "web"
		}
		return nil, v.s.OpenAgent(q.ID, "", q.UI)
	case "schedule_save":
		var x scheduler.Schedule
		if e := json.Unmarshal(raw, &x); e != nil {
			return nil, e
		}
		return v.s.SaveSchedule(x)
	case "schedule_pause":
		return nil, v.s.PauseSchedule(q.ID, true)
	case "schedule_resume":
		return nil, v.s.PauseSchedule(q.ID, false)
	case "schedule_delete":
		return nil, v.s.DeleteSchedule(q.ID)
	case "schedule_run":
		return v.s.RunNow(q.ID)
	case "run_stop":
		return nil, v.s.StopRun(q.ID)
	case "run_log":
		return v.s.RunLog(q.ID)
	case "run_open":
		if q.UI == "" {
			q.UI = "web"
		}
		return nil, v.s.OpenAgent("", q.ID, q.UI)
	case "pause_all", "resume_all", "quiet_toggle", "notifications_toggle":
		cfg := v.s.Snapshot().Settings
		switch op {
		case "pause_all":
			cfg.Paused = true
		case "resume_all":
			cfg.Paused = false
		case "quiet_toggle":
			cfg.Quiet = !cfg.Quiet
		case "notifications_toggle":
			cfg.Notifications = !cfg.Notifications
		}
		return nil, v.s.SetSettings(cfg)
	case "stop_all":
		return nil, v.s.StopAll()
	case "notifications_read":
		return nil, v.s.ReadNotifications()
	case "history_clear":
		return nil, v.s.ClearHistory()
	case "settings_save":
		var cfg scheduler.Settings
		if e := json.Unmarshal(raw, &cfg); e != nil {
			return nil, e
		}
		old := v.s.Snapshot().Settings
		cfg.WebEnabled = old.WebEnabled
		cfg.Startup = old.Startup
		return nil, v.s.SetSettings(cfg)
	case "assistant_key":
		var x struct {
			Key string `json:"key"`
		}
		if e := json.Unmarshal(raw, &x); e != nil {
			return nil, e
		}
		return nil, v.s.SetKey(x.Key)
	case "chat_clear":
		return nil, v.AI.Clear()
	case "web_start":
		e := v.StartWeb()
		return v.WebURL(), e
	case "web_stop":
		return nil, v.StopWeb()
	case "web_open":
		return nil, v.Open()
	case "startup_enable", "startup_disable":
		on := op == "startup_enable"
		if e := platform.Startup(v.exe, v.s.Home(), on); e != nil {
			return nil, e
		}
		cfg := v.s.Snapshot().Settings
		cfg.Startup = on
		return nil, v.s.SetSettings(cfg)
	case "shutdown":
		if v.Shutdown != nil {
			go v.Shutdown()
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown operation %q", op)
}
