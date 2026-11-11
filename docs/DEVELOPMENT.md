# Developer workflow

## Principles

1. **Small, verifiable increments.** Each increment in [PLAN.md](PLAN.md) ends with a manual check
   the owner performs on real hardware. The next one does not start until the check passes.
2. **Tests ship with the code.** Every Go package has unit tests; adapters that talk to external
   processes get contract tests against fakes. `make check` must pass before a commit.
3. **Decisions are written down.** Anything that is not obvious from the code gets an ADR in
   `docs/adr/` (why go2rtc, why Tailscale, what happened with macOS camera permissions, ...).
4. **No secrets in git.** Shared secret, VAPID keys and Tailscale details live in `*.local.yaml`
   or environment variables; `.gitignore` covers them.

## Daily loop

```sh
make check        # fmt + vet + tests
make run          # API on :8080
make web-dev      # client on :5173 with /api proxied
```

Commit per increment (or per meaningful step inside one) with a message of the form
`inc<N>: <what changed>`. Push to the private GitHub repo `maikomr/watchmydog`.

## Code organisation (hexagonal)

- `internal/domain` – entities and value objects, no imports from adapters.
- `internal/ports` – interfaces the domain needs from the outside world
  (StreamEngine, SoundDetector, Notifier, Recorder, EventStore, Authenticator, CameraControl).
- `internal/adapters/<port>/<impl>` – one package per concrete implementation.
- `internal/httpapi` – HTTP handlers; translate requests into port calls, no business rules.
- `cmd/watchmydog` – wiring and process lifecycle only.

Rule of thumb: if a package imports `os/exec`, `net`, `database/sql` or a third-party SDK, it is an
adapter and must sit behind a port.

## Verification log

Record the outcome of each increment's manual check here, with the date, so regressions are easy to
spot later.

| Inc | Date | Result | Notes |
|-----|------|--------|-------|
| 0 | 2026-11-11 | pass | `make check` green, `/api/health` returns ok |
