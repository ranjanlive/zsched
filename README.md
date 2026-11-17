# zsched

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20Linux%20%7C%20macOS-lightgrey)](#cross-compilation--releases)

A local, lightweight **agent task scheduler and process supervisor** with a native Windows system tray, embedded web dashboard, headless CLI, and an OpenAI-compatible scheduling assistant.

Written in pure Go with **zero external dependencies** — no Node.js, Python, Electron, Docker, or external database required.

---

## Highlights

- **Native Windows System Tray:** Double-click `zsched-tray.exe` to launch a windowless background daemon with a responsive tray icon menu, notification balloons, and optional per-user Windows login startup.
- **Embedded Web Dashboard:** Fast, responsive web UI served directly from memory with live task progress, logs viewer, schedule timers, and agent status.
- **Process Supervision & Confinement:** Uses native Windows Job Objects (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`) and Unix process groups to guarantee that child agent process trees are terminated completely when stopped.
- **Flexible Scheduling Triggers:**
  - Fixed intervals (e.g., `--every 2h`)
  - Random intervals within bounds (e.g., `--random 20h:30h`)
  - Specific timestamps (`--at 2026-12-01T09:00:00+05:30`)
  - Daily scheduled times (`--daily 09:00 --timezone Asia/Kolkata`)
  - Weekly days with exact time or random time windows (`--weekly sat --window 00:00-23:59`)
- **Crash Recovery & Side-Effect Safety:** Tasks interrupted by power loss or restarts are marked `interrupted` in durable state and are never blindly re-executed.
- **Autonomous Scheduling Assistant:** Includes a built-in assistant powered by any OpenAI-compatible Chat Completions model to inspect, create, edit, pause, and supervise tasks using tool calls.
- **Compatible Agent Contract:** Supervised agents follow the standard `zagent` CLI contract:
  ```text
  agent --home HOME --workspace WORKSPACE --session SESSION --prompt-file TASK_FILE --verbose
  ```

---

## Quick Start on Windows

### 1. Launch System Tray
Double-click `zsched-tray.exe` or run:

```powershell
.\zsched-tray.exe
```
This starts the daemon in the background and opens the embedded dashboard in your default browser (`http://127.0.0.1:8090`).

### 2. Command-Line Controls
Use `zsched.exe` for terminal operations:

```powershell
# Check daemon status
.\zsched.exe status

# Open web dashboard or terminal UI
.\zsched.exe ui web
.\zsched.exe ui cli

# Pause / Resume automatic dispatch
.\zsched.exe pause
.\zsched.exe resume
```

---

## Registering an Agent

Register any external agent executable implementing the standard CLI contract:

```powershell
# Register zagent with up to 2 concurrent runs
.\zsched.exe agents add --name "ResearchAgent" --binary "C:\tools\zagent.exe" --parallel 2

# List registered agents
.\zsched.exe agents list
```

---

## Scheduling Tasks

```powershell
# Run a task every 2 to 4 hours (randomly sampled, persists across restart)
.\zsched.exe schedules add --name "HealthCheck" --agent AGENT_ID --task "Run system checks" --random 2h:4h

# Run every Saturday within a random time window
.\zsched.exe schedules add --name "WeeklyAudit" --agent AGENT_ID --task "Generate weekly changelog" --weekly sat --window 10:00-18:00 --timezone Asia/Kolkata

# Daily at 9:00 AM
.\zsched.exe schedules add --name "DailyBriefing" --agent AGENT_ID --task "Compile daily news" --daily 09:00 --timezone Asia/Kolkata

# One-time execution at a future date
.\zsched.exe schedules add --name "DeployRelease" --agent AGENT_ID --task "Tag and publish release" --at 2026-12-01T10:00:00+05:30

# List, pause, or manually trigger a run
.\zsched.exe schedules list
.\zsched.exe schedules pause SCHEDULE_ID
.\zsched.exe schedules resume SCHEDULE_ID
.\zsched.exe schedules run SCHEDULE_ID
```

---

## Built-in Scheduling Assistant

Configure an OpenAI-compatible endpoint (Ollama, vLLM, OpenRouter, OpenAI) in Settings, then interact naturally:

```powershell
# Ask the assistant to inspect or schedule tasks
.\zsched.exe ask "List all active schedules and tell me which one runs next"
.\zsched.exe ask "Schedule a backup task every night at 2 AM"
```

---

## CLI Reference

```text
zsched · local agent automation

  zsched                         Start background scheduler and open dashboard
  zsched daemon [--tray]         Run scheduler in foreground (Windows tray optional)
  zsched start | status | stop   Manage background daemon
  zsched ui web | cli            Open web dashboard or interactive terminal
  zsched web start | stop        Control optional dashboard server
  zsched agents list             List registered agents
  zsched agents add              Register a new agent binary
  zsched agents remove ID        Remove an agent with no active schedules
  zsched schedules list          List all schedules with next run times
  zsched schedules add           Add a new scheduled task
  zsched schedules run ID        Manually dispatch a schedule now
  zsched schedules pause ID      Pause a schedule
  zsched schedules resume ID     Resume a paused schedule
  zsched runs list               View active, completed, and interrupted runs
  zsched runs logs ID            Tail log output for a specific run
  zsched runs stop ID            Immediately stop and terminate a running task
  zsched pause | resume          Globally pause or resume all task dispatching
  zsched quiet on | off          Toggle notification popups
  zsched ask "..."               Execute assistant natural language automation
  zsched export FILE             Export schedules, agents, and configuration
  zsched import FILE             Import schedules and agents
  zsched startup on | off        Register or remove Windows login startup
  zsched version                 Display binary version

Global Option:
  --home DIR                     Data directory (default: <binary_dir>/data, or ZSCHED_HOME)
```

---

## Cross-Compilation & Releases

Build standalone binaries for Windows, Linux, and macOS:

```powershell
# Build Windows CLI and windowless System Tray binary
.\scripts\build.ps1

# Cross-compile for all 6 target platforms
.\scripts\build.ps1 -All

# Create release package with checksums
.\scripts\package.ps1 -Version 0.1.0
```

Compiled distributions:
- `zsched.exe` — Windows x64 console executable
- `zsched-tray.exe` — Windows x64 silent system tray executable
- `zsched-windows-arm64.exe` — Windows ARM64 executable
- `zsched-linux-amd64` — Linux x86_64 binary
- `zsched-linux-arm64` — Linux ARM64 binary
- `zsched-darwin-amd64` — macOS Intel binary
- `zsched-darwin-arm64` — macOS Apple Silicon binary

---

## Testing

Run the automated test suite across scheduler dispatch, trigger calculations, assistant tools, and integration contracts:

```bash
go test -v ./...
go vet ./...
```

---

## License

Distributed under the **Apache License, Version 2.0**. See [LICENSE](LICENSE) for details.
