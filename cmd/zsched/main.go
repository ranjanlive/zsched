package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"
	"zsched/internal/platform"
	"zsched/internal/scheduler"
	"zsched/internal/web"
)

const help = `zsched · local agent automation

  zsched                         Start background scheduler and open dashboard
  zsched daemon [--tray]          Run scheduler in the foreground (Windows tray optional)
  zsched start | status | stop    Manage the background daemon
  zsched ui web | cli            Open dashboard or interactive terminal
  zsched web start | stop        Control the optional dashboard server
  zsched agents list             List registered agents
  zsched agents add --name NAME --binary PATH [--description TEXT]
         [--agent-home DIR] [--workspace DIR] [--parallel N]
  zsched agents edit FILE         Update an agent using a JSON file
  zsched agents remove ID         Remove an agent with no schedules or pending runs
  zsched agents open ID [cli]     Open the agent's own interface
  zsched schedules list
  zsched schedules add --name NAME --agent ID --task TEXT
         --every 2h | --random 20h:30h | --at RFC3339
         --daily 09:00 | --weekly sat --time 09:00
         --weekly sat --window 00:00-23:59
         [--timezone Asia/Kolkata] [--timeout 60] [--retries 0]
         [--overlap skip|queue] [--missed run_once|skip] [--session fresh|persistent]
  zsched schedules edit FILE      Update a schedule using a JSON file
  zsched schedules run | pause | resume | remove ID
  zsched runs list | logs ID | stop ID | open ID
  zsched pause | resume | stop-runs
  zsched quiet on|off | notifications on|off|list
  zsched ask "Schedule a task..." Built-in assistant (configure via Settings)
  zsched settings                Print settings without API keys
  zsched export FILE             Export registry, schedules and settings without keys/history
  zsched import FILE             Import agents and schedules (creates new IDs)
  zsched startup on | off         Register or remove Windows login startup
  zsched version

Global option: --home DIR (anywhere). Default: <binary folder>/data, or ZSCHED_HOME.
Registered binaries must support zagent's --home/--workspace/--session/--prompt-file/--ui flags.
`

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "zsched:", e)
		os.Exit(1)
	}
}
func homeArgs(args []string) (string, []string, error) {
	home := os.Getenv("ZSCHED_HOME")
	out := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--home" {
			if i+1 >= len(args) {
				return "", nil, errors.New("--home needs a directory")
			}
			i++
			home = args[i]
		} else if strings.HasPrefix(args[i], "--home=") {
			home = strings.TrimPrefix(args[i], "--home=")
		} else {
			out = append(out, args[i])
		}
	}
	if home == "" {
		exe, _ := os.Executable()
		home = filepath.Join(filepath.Dir(exe), "data")
	}
	a, e := filepath.Abs(home)
	return a, out, e
}
func run(args []string) error {
	home, args, e := homeArgs(args)
	if e != nil {
		return e
	}
	if len(args) == 0 {
		args = []string{"ui", "web"}
	}
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Print(help)
		return nil
	case "version":
		fmt.Println("zsched", scheduler.Version)
		return nil
	case "daemon":
		return daemon(home, args[1:])
	case "start":
		if e = ensure(home); e == nil {
			fmt.Println("Background scheduler is running.")
		}
		return e
	case "stop":
		_, e = request(home, "POST", "/api/action", map[string]any{"operation": "shutdown", "payload": map[string]any{}})
		return e
	}
	if args[0] == "status" {
		x, e := request(home, "GET", "/api/state", nil)
		if e != nil {
			return e
		}
		return pretty(x)
	}
	if e = ensure(home); e != nil {
		return e
	}
	if args[0] == "ui" {
		mode := "web"
		if len(args) > 1 {
			mode = args[1]
		}
		if mode == "cli" {
			return console(home)
		}
		if mode != "web" {
			return errors.New("interface must be cli or web")
		}
		x, e := act(home, "web_start", nil)
		if e != nil {
			return e
		}
		var url string
		b, _ := json.Marshal(x)
		json.Unmarshal(b, &url)
		fmt.Println(url)
		return platform.OpenURL(url)
	}
	if args[0] == "ask" {
		if len(args) < 2 {
			return errors.New("give the assistant a message")
		}
		x, e := request(home, "POST", "/api/chat", map[string]string{"text": strings.Join(args[1:], " ")})
		if e != nil {
			return e
		}
		return pretty(x)
	}
	switch args[0] {
	case "pause", "resume", "stop-runs":
		op := map[string]string{"pause": "pause_all", "resume": "resume_all", "stop-runs": "stop_all"}[args[0]]
		_, e = act(home, op, nil)
		return e
	case "web":
		if len(args) < 2 {
			return errors.New("use web start or web stop")
		}
		if args[1] != "start" && args[1] != "stop" {
			return errors.New("use web start or web stop")
		}
		x, e := act(home, "web_"+args[1], nil)
		if e == nil {
			pretty(x)
		}
		return e
	case "startup":
		if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
			return errors.New("use startup on or off")
		}
		op := "startup_enable"
		if args[1] == "off" {
			op = "startup_disable"
		}
		_, e = act(home, op, nil)
		return e
	case "quiet", "notifications":
		return preference(home, args)
	case "agents":
		return agentCmd(home, args[1:])
	case "schedules":
		return scheduleCmd(home, args[1:])
	case "runs":
		return runCmd(home, args[1:])
	case "settings":
		st, e := state(home)
		if e != nil {
			return e
		}
		return pretty(st.Settings)
	case "export":
		if len(args) != 2 {
			return errors.New("export needs a filename")
		}
		st, e := state(home)
		if e != nil {
			return e
		}
		st.Runs = nil
		st.Notifications = nil
		st.Audit = nil
		st.Settings.AI.KeySet = false
		st.Settings.WebEnabled = false
		st.Settings.Startup = false
		b, _ := json.MarshalIndent(st, "", "  ")
		return scheduler.Atomic(args[1], b)
	case "import":
		if len(args) != 2 {
			return errors.New("import needs a filename")
		}
		return importFile(home, args[1])
	}
	return fmt.Errorf("unknown command %q; run zsched help", args[0])
}
func request(home, method, path string, body any) (any, error) {
	b, e := os.ReadFile(filepath.Join(home, "daemon.json"))
	if e != nil {
		return nil, errors.New("scheduler is not running; run zsched start")
	}
	var ep web.Endpoint
	if e = json.Unmarshal(b, &ep); e != nil {
		return nil, e
	}
	var rd io.Reader
	if body != nil {
		b, e = json.Marshal(body)
		if e != nil {
			return nil, e
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, "http://"+ep.Addr+path, rd)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	r, e := http.DefaultClient.Do(req)
	if e != nil {
		return nil, errors.New("cannot reach scheduler; run zsched start")
	}
	defer r.Body.Close()
	var out any
	if e = json.NewDecoder(io.LimitReader(r.Body, 32*1024*1024)).Decode(&out); e != nil {
		return nil, e
	}
	if r.StatusCode/100 != 2 {
		m, _ := out.(map[string]any)
		return nil, fmt.Errorf("%v", m["error"])
	}
	return out, nil
}
func act(home, op string, payload any) (any, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	x, e := request(home, "POST", "/api/action", map[string]any{"operation": op, "payload": payload})
	if e != nil {
		return nil, e
	}
	return x.(map[string]any)["result"], nil
}
func state(home string) (scheduler.State, error) {
	x, e := request(home, "GET", "/api/state", nil)
	if e != nil {
		return scheduler.State{}, e
	}
	b, _ := json.Marshal(x.(map[string]any)["state"])
	var st scheduler.State
	e = json.Unmarshal(b, &st)
	return st, e
}
func pretty(x any) error {
	b, e := json.MarshalIndent(x, "", "  ")
	if e == nil {
		fmt.Println(string(b))
	}
	return e
}
func ensure(home string) error {
	if _, e := os.Stat(filepath.Join(home, "daemon.json")); e == nil {
		b, _ := os.ReadFile(filepath.Join(home, "daemon.json"))
		var ep web.Endpoint
		json.Unmarshal(b, &ep)
		c := http.Client{Timeout: 500 * time.Millisecond}
		req, _ := http.NewRequest("GET", "http://"+ep.Addr+"/api/state", nil)
		if req != nil {
			req.Header.Set("Authorization", "Bearer "+ep.Token)
			if r, e := c.Do(req); e == nil {
				r.Body.Close()
				if r.StatusCode == 200 {
					return nil
				}
			}
		}
	}
	if e := os.MkdirAll(home, 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(home, "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	exe, _ := os.Executable()
	args := []string{"daemon", "--home", home}
	if runtime.GOOS == "windows" {
		args = append(args, "--tray")
	}
	cmd := exec.Command(exe, args...)
	platform.Configure(cmd)
	cmd.Stdout = f
	cmd.Stderr = f
	if e = cmd.Start(); e != nil {
		return e
	}
	go cmd.Wait()
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, e = requestFast(home); e == nil {
			return nil
		}
	}
	return fmt.Errorf("scheduler did not start; inspect %s", filepath.Join(home, "daemon.log"))
}
func requestFast(home string) (bool, error) {
	b, e := os.ReadFile(filepath.Join(home, "daemon.json"))
	if e != nil {
		return false, e
	}
	var ep web.Endpoint
	json.Unmarshal(b, &ep)
	req, e := http.NewRequest("GET", "http://"+ep.Addr+"/api/state", nil)
	if e != nil {
		return false, e
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	r, e := (&http.Client{Timeout: 200 * time.Millisecond}).Do(req)
	if e != nil {
		return false, e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return false, errors.New("not ready")
	}
	return true, nil
}
func daemon(home string, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	tray := fs.Bool("tray", false, "Show Windows tray icon")
	openWeb := fs.Bool("web", false, "Start dashboard server")
	if e := fs.Parse(args); e != nil {
		return e
	}
	if e := os.MkdirAll(home, 0700); e != nil {
		return e
	}
	lock, e := platform.Acquire(filepath.Join(home, "daemon.lock"))
	if e != nil {
		return e
	}
	defer lock.Close()
	s, e := scheduler.New(home)
	if e != nil {
		return e
	}
	defer s.Close()
	v, e := web.New(s)
	if e != nil {
		return e
	}
	defer v.Close()
	done := make(chan struct{})
	var once sync.Once
	quit := func() { once.Do(func() { close(done) }) }
	v.Shutdown = quit
	if e = v.Start(); e != nil {
		return e
	}
	if *openWeb {
		if e = v.StartWeb(); e != nil {
			return e
		}
	}
	s.OnNotification = func(n scheduler.Notification) { platform.Balloon(n.Title, n.Body) }
	s.Start()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			quit()
		case <-done:
		}
	}()
	fmt.Println("zsched is running. Home:", home)
	if *tray && runtime.GOOS == "windows" {
		trayErr := platform.Tray(func() []platform.MenuItem { return trayMenu(s, v) }, func(op string) {
			if strings.HasPrefix(op, "agent:") {
				v.Action("agent_open", json.RawMessage(fmt.Sprintf(`{"id":%q,"ui":"web"}`, strings.TrimPrefix(op, "agent:"))))
				return
			}
			if strings.HasPrefix(op, "run:") {
				v.Action("run_open", json.RawMessage(fmt.Sprintf(`{"id":%q,"ui":"web"}`, strings.TrimPrefix(op, "run:"))))
				return
			}
			if _, e := v.Action(op, nil); e != nil {
				platform.Balloon("zsched", e.Error())
			}
		}, done)
		if trayErr == nil {
			return nil
		}
		fmt.Fprintln(os.Stderr, "Tray unavailable; continuing headless:", trayErr)
	}
	<-done
	return nil
}
func trayMenu(s *scheduler.Service, v *web.Server) []platform.MenuItem {
	st := s.Snapshot()
	n := 0
	for _, r := range st.Runs {
		if r.State == "running" || r.State == "starting" {
			n++
		}
	}
	pauseLabel, op := "Pause all schedules", "pause_all"
	if st.Settings.Paused {
		pauseLabel, op = "Resume all schedules", "resume_all"
	}
	menu := []platform.MenuItem{{Label: fmt.Sprintf("zsched · %d agents · %d active runs", len(st.Agents), n), Disabled: true}, {Separator: true}, {Label: "Open dashboard", Action: "web_open"}, {Label: "Start web server", Action: "web_start", Disabled: v.WebURL() != ""}, {Label: "Stop web server", Action: "web_stop", Disabled: v.WebURL() == ""}, {Separator: true}, {Label: pauseLabel, Action: op}, {Label: "Stop all active and queued runs", Action: "stop_all"}, {Label: "Quiet mode", Action: "quiet_toggle", Checked: st.Settings.Quiet}, {Label: "Desktop notifications", Action: "notifications_toggle", Checked: st.Settings.Notifications}, {Separator: true}}
	for _, a := range st.Agents {
		menu = append(menu, platform.MenuItem{Label: "Open agent: " + a.Name, Action: "agent:" + a.ID})
	}
	for _, r := range st.Runs {
		if r.State == "running" || r.State == "starting" {
			menu = append(menu, platform.MenuItem{Label: "Open run: " + r.ScheduleName, Action: "run:" + r.ID})
		}
	}
	menu = append(menu, platform.MenuItem{Separator: true}, platform.MenuItem{Label: "Exit scheduler (stop workers)", Action: "shutdown"})
	return menu
}
func parseFile(file string) (any, error) {
	b, e := os.ReadFile(file)
	if e != nil {
		return nil, e
	}
	var x any
	e = json.Unmarshal(b, &x)
	return x, e
}
func agentCmd(home string, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		st, e := state(home)
		if e != nil {
			return e
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATUS\tPARALLEL\tWORKSPACE")
		for _, a := range st.Agents {
			fmt.Fprintf(w, "%s\t%s\t%t\t%d\t%s\n", a.ID, a.Name, a.Enabled, a.MaxParallel, a.Workspace)
		}
		return w.Flush()
	}
	if args[0] == "add" {
		fs := flag.NewFlagSet("agents add", flag.ContinueOnError)
		var a scheduler.Agent
		fs.StringVar(&a.Name, "name", "", "Name")
		fs.StringVar(&a.Description, "description", "", "Description")
		fs.StringVar(&a.Binary, "binary", "", "Binary")
		fs.StringVar(&a.Home, "agent-home", "", "Agent home")
		fs.StringVar(&a.Workspace, "workspace", "", "Workspace")
		fs.IntVar(&a.MaxParallel, "parallel", 1, "Parallel runs")
		if e := fs.Parse(args[1:]); e != nil {
			return e
		}
		x, e := act(home, "agent_save", a)
		if e == nil {
			pretty(x)
		}
		return e
	}
	if len(args) < 2 {
		return errors.New("agent ID or JSON file is required")
	}
	if args[0] == "edit" {
		x, e := parseFile(args[1])
		if e != nil {
			return e
		}
		_, e = act(home, "agent_save", x)
		return e
	}
	op := map[string]string{"remove": "agent_delete", "open": "agent_open"}[args[0]]
	if op == "" {
		return errors.New("unknown agent command")
	}
	ui := "web"
	if len(args) > 2 {
		ui = args[2]
	}
	_, e := act(home, op, map[string]string{"id": args[1], "ui": ui})
	return e
}
func scheduleCmd(home string, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		st, e := state(home)
		if e != nil {
			return e
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tENABLED\tTYPE\tNEXT RUN")
		for _, x := range st.Schedules {
			fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\n", x.ID, x.Name, x.Enabled, x.Trigger.Kind, x.NextRun.Local().Format(time.RFC3339))
		}
		return w.Flush()
	}
	if args[0] == "add" {
		return addSchedule(home, args[1:])
	}
	if len(args) < 2 {
		return errors.New("schedule ID or JSON file is required")
	}
	if args[0] == "edit" {
		x, e := parseFile(args[1])
		if e != nil {
			return e
		}
		_, e = act(home, "schedule_save", x)
		return e
	}
	op := map[string]string{"run": "schedule_run", "pause": "schedule_pause", "resume": "schedule_resume", "remove": "schedule_delete"}[args[0]]
	if op == "" {
		return errors.New("unknown schedule command")
	}
	x, e := act(home, op, map[string]string{"id": args[1]})
	if e == nil && x != nil {
		pretty(x)
	}
	return e
}
func addSchedule(home string, args []string) error {
	fs := flag.NewFlagSet("schedules add", flag.ContinueOnError)
	var x scheduler.Schedule
	var every, randomRange, at, daily, weekly, clock, window string
	fs.StringVar(&x.Name, "name", "", "Name")
	fs.StringVar(&x.AgentID, "agent", "", "Agent ID")
	fs.StringVar(&x.Task, "task", "", "Task")
	fs.StringVar(&every, "every", "", "Fixed interval")
	fs.StringVar(&randomRange, "random", "", "Random interval min:max")
	fs.StringVar(&at, "at", "", "One-time RFC3339 timestamp")
	fs.StringVar(&daily, "daily", "", "Daily HH:MM")
	fs.StringVar(&weekly, "weekly", "", "Comma-separated weekdays")
	fs.StringVar(&clock, "time", "09:00", "Weekly HH:MM")
	fs.StringVar(&window, "window", "", "Random HH:MM-HH:MM window")
	fs.StringVar(&x.Trigger.Timezone, "timezone", "Local", "Timezone")
	fs.IntVar(&x.TimeoutMinutes, "timeout", 60, "Runtime minutes")
	fs.IntVar(&x.RetryCount, "retries", 0, "Retries")
	fs.StringVar(&x.Overlap, "overlap", "skip", "Overlap")
	fs.StringVar(&x.Missed, "missed", "run_once", "Missed runs")
	fs.StringVar(&x.SessionMode, "session", "fresh", "Session mode")
	if e := fs.Parse(args); e != nil {
		return e
	}
	count := 0
	for _, s := range []string{every, randomRange, at, daily, weekly} {
		if s != "" {
			count++
		}
	}
	if count != 1 {
		return errors.New("choose exactly one of --every, --random, --at, --daily or --weekly")
	}
	var e error
	switch {
	case every != "":
		x.Trigger.Kind = "interval"
		x.Trigger.EverySeconds, e = seconds(every)
	case randomRange != "":
		p := strings.Split(randomRange, ":")
		if len(p) != 2 {
			return errors.New("random range uses min:max, for example 20h:30h")
		}
		x.Trigger.Kind = "random_interval"
		x.Trigger.MinSeconds, e = seconds(p[0])
		if e == nil {
			x.Trigger.MaxSeconds, e = seconds(p[1])
		}
	case at != "":
		x.Trigger.Kind = "once"
		x.Trigger.At, e = time.Parse(time.RFC3339, at)
	case daily != "":
		x.Trigger.Kind = "daily"
		x.Trigger.Time = daily
	case weekly != "":
		x.Trigger.Kind = "weekly"
		x.Trigger.Time = clock
		names := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
		for _, d := range strings.Split(strings.ToLower(weekly), ",") {
			found := false
			for i, n := range names {
				if d == n {
					x.Trigger.Days = append(x.Trigger.Days, i)
					found = true
				}
			}
			if !found {
				return fmt.Errorf("unknown weekday %q", d)
			}
		}
		if window != "" {
			parts := strings.Split(window, "-")
			if len(parts) != 2 {
				return errors.New("window uses HH:MM-HH:MM")
			}
			x.Trigger.Kind = "random_weekly"
			x.Trigger.WindowStart = parts[0]
			x.Trigger.WindowEnd = parts[1]
		}
	}
	if e != nil {
		return e
	}
	v, e := act(home, "schedule_save", x)
	if e == nil {
		pretty(v)
	}
	return e
}
func seconds(s string) (int64, error) {
	if strings.HasSuffix(s, "d") {
		n, e := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if e != nil {
			return 0, e
		}
		return int64(n * 86400), nil
	}
	d, e := time.ParseDuration(s)
	return int64(d / time.Second), e
}
func runCmd(home string, args []string) error {
	if len(args) == 0 || args[0] == "list" {
		st, e := state(home)
		if e != nil {
			return e
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tSCHEDULE\tSTATE\tSTARTED")
		for i := len(st.Runs) - 1; i >= 0; i-- {
			r := st.Runs[i]
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.ScheduleName, r.State, r.StartedAt.Local().Format(time.RFC3339))
		}
		return w.Flush()
	}
	if len(args) < 2 {
		return errors.New("run ID is required")
	}
	op := map[string]string{"logs": "run_log", "stop": "run_stop", "open": "run_open"}[args[0]]
	if op == "" {
		return errors.New("unknown run command")
	}
	x, e := act(home, op, map[string]string{"id": args[1], "ui": "web"})
	if e == nil && x != nil {
		fmt.Println(x)
	}
	return e
}
func preference(home string, args []string) error {
	st, e := state(home)
	if e != nil {
		return e
	}
	if len(args) == 2 && args[0] == "notifications" && args[1] == "list" {
		return pretty(st.Notifications)
	}
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return errors.New("choose on or off")
	}
	on := args[1] == "on"
	if args[0] == "quiet" {
		st.Settings.Quiet = on
	} else {
		st.Settings.Notifications = on
	}
	_, e = act(home, "settings_save", st.Settings)
	return e
}
func importFile(home, file string) error {
	x, e := parseFile(file)
	if e != nil {
		return e
	}
	b, _ := json.Marshal(x)
	var st scheduler.State
	if e = json.Unmarshal(b, &st); e != nil {
		return e
	}
	ids := map[string]string{}
	for _, a := range st.Agents {
		old := a.ID
		a.ID = ""
		v, e := act(home, "agent_save", a)
		if e != nil {
			return fmt.Errorf("import agent %s: %w", a.Name, e)
		}
		ids[old] = v.(map[string]any)["id"].(string)
	}
	for _, x := range st.Schedules {
		x.ID = ""
		x.AgentID = ids[x.AgentID]
		if x.Trigger.Kind == "once" && !x.Trigger.At.After(time.Now()) {
			continue
		}
		if _, e = act(home, "schedule_save", x); e != nil {
			return fmt.Errorf("import schedule %s: %w", x.Name, e)
		}
	}
	fmt.Println("Agents and schedules imported. Existing settings were preserved.")
	return nil
}
func console(home string) error {
	in := bufio.NewScanner(os.Stdin)
	for {
		st, e := state(home)
		if e != nil {
			return e
		}
		fmt.Print("\033[2J\033[H\033[38;5;115m  zsched  /  CONTROL ROOM\033[0m\n\n")
		n := 0
		for _, r := range st.Runs {
			if r.State == "running" || r.State == "starting" {
				n++
			}
		}
		fmt.Printf("  %d agents    %d schedules    %d active runs    paused: %t\n\n", len(st.Agents), len(st.Schedules), n, st.Settings.Paused)
		fmt.Print("  1 Agents    2 Schedules    3 Runs    4 Open dashboard\n  5 Pause all 6 Resume all   7 Stop runs 8 Assistant\n  Type a CLI command, refresh, or exit to detach.\n")
		fmt.Print("  zsched › ")
		if !in.Scan() {
			return in.Err()
		}
		line := strings.TrimSpace(in.Text())
		if line == "exit" || line == "q" {
			return nil
		}
		if line == "" || line == "refresh" {
			continue
		}
		commands := map[string][]string{"1": {"agents", "list"}, "2": {"schedules", "list"}, "3": {"runs", "list"}, "4": {"ui", "web"}, "5": {"pause"}, "6": {"resume"}, "7": {"stop-runs"}}
		if line == "8" {
			fmt.Print("  Ask › ")
			if !in.Scan() {
				return nil
			}
			commands[line] = []string{"ask", in.Text()}
		}
		cmd, ok := commands[line]
		if !ok {
			cmd = strings.Fields(line)
		}
		if len(cmd) > 0 && cmd[0] == "ui" && len(cmd) > 1 && cmd[1] == "cli" {
			continue
		}
		if e = run(append([]string{"--home", home}, cmd...)); e != nil {
			fmt.Println(" ", e)
		}
		fmt.Print("\n  Enter to continue…")
		if !in.Scan() {
			return nil
		}
	}
}
