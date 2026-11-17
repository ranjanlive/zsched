# zsched implementation plan

A local-first, single-binary scheduler with a Windows tray, authenticated optional web dashboard, headless CLI and interactive console. Defaults: agent home is its binary directory; workspace is home/workspace; one parallel run per agent. OS timezone unless a schedule specifies another.

## Delivery stages
1. Durable state, OS process lock, schedule calculations and registry validation.
2. Scheduler dispatch, bounded worker processes, unique sessions, cancellation, missed-run policies, logs and history.
3. Authenticated daemon API and CLI: register/edit/remove agents, create/edit/pause/run schedules, stop runs, pause all, notifications and quiet mode.
4. Responsive dashboard: Overview, Agents, Schedules, Runs, Assistant and Settings, with accessible dialogs, running animations and next-run countdowns.
5. Windows tray: dashboard server on/off, pause/resume, stop workers, notifications, quiet mode, agent interfaces and clean exit. Login startup integration.
6. OpenAI-compatible assistant: configurable endpoint/key/model/context/compact threshold, durable conversations and tools for every scheduler operation.
7. Automated schedule, persistence, API, worker and assistant tests; platform builds; browser and runtime smoke tests; documentation and packaging.

## Execution contract
Registered agents follow zagent flags (--home, --workspace, --session, --prompt-file, --ui). Every scheduled occurrence has a unique durable run and session ID. Schedules persist their sampled next occurrence, so random times do not move on restart. Pause blocks future dispatch without killing existing work; stop explicitly cancels processes. Crash recovery marks uncertain runs interrupted instead of blindly duplicating side effects. CLI commands require the daemon for mutations; only the daemon owns state.

## Limits
Unattended scheduling can work only while the machine is awake. The scheduler uses no model calls when idle. Model-side schedule modifications are explicitly authorized by the user request, but credentials are never returned to model tools. External agent command permissions remain configured in the agent home. Windows tray is native; non-Windows targets expose CLI/web only.
