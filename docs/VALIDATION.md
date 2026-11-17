# Validation — 2026-10-12

The 0.1 implementation was validated on Windows AMD64 using Go 1.27 with CGO disabled.

- `go test ./...`: 12 meaningful tests passed across scheduler, API and assistant packages.
- `go vet ./...`: passed.
- Native Windows console/tray binaries and cross builds for Windows ARM64, Linux AMD64/ARM64, macOS AMD64/ARM64: passed. Non-Windows builds were compiled, not executed on those operating systems.
- Trigger checks cover interval, random interval bounds, one-time, weekday/window timing, IANA zones, invalid inputs, and daylight-saving missing wall times.
- State checks cover atomic-write rollback, interrupted-run recovery, exclusive daemon lock, key encryption round trip, and persisted random samples across edits/restart.
- Dispatch checks cover global limits, agent limits, persistent-session serialization, paused queued work and cancellation.
- A real compiled fake agent was launched through the production flag contract. Success, logs, cancellation and worker supervision passed.
- A fake Chat Completions endpoint validated assistant function execution and scheduled internal-assistant tasks without contacting a model provider.
- API checks cover required tokens, unexpected hosts, hostile origins, and independent dashboard shutdown with continuing CLI access.
- Browser checks verified responsive dashboard rendering, agent registration/default directories, random 20–30 hour scheduling, individual pause, manual run, running indicator, success history, logs and built-in agent registration controls.
- A Windows tray daemon was launched successfully. Native tray menu appearance and desktop balloon appearance have not been visually inspected in this environment.

No real model credentials or real coding-agent task execution were used. No login startup entry was installed during QA. Existing scheduler processes from other installations were left alone.

Remaining operational limits are documented in README: agent CLI compatibility, awake-machine scheduling, estimated token counts, provider-specific function-tool interoperability, record-only history retention, partial CLI imports, Windows-only tray/startup, and Unix process-group supervision.
