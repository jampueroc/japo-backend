# japo-backend

REST API for a personal language-learning web app: user authentication and
per-user progress. Written in Go with Fiber and MariaDB, developed natively on
macOS and deployed as a static `linux/arm64` binary in Docker on a Raspberry
Pi.

---

## Table of contents

- [Stack](#stack)
- [Architecture](#architecture)
- [API](#api)
- [Run locally on macOS](#run-locally-on-macos)
- [Configuration](#configuration)
- [Testing](#testing)
- [Deploy to Raspberry Pi](#deploy-to-raspberry-pi)
- [Make targets](#make-targets)

---

## Stack

| Concern      | Choice                                                     |
| ------------ | ---------------------------------------------------------- |
| HTTP         | [Fiber v2](https://gofiber.io)                             |
| Database     | MariaDB 11.4 (same engine in dev, test and prod)           |
| DB access    | `database/sql` + `go-sql-driver/mysql` (pure Go, no CGO)   |
| Migrations   | [goose](https://github.com/pressly/goose), embedded via `embed.FS` |
| Auth         | `golang-jwt/jwt/v5`, HS256, 1 h access token, no refresh   |
| Passwords    | bcrypt (`golang.org/x/crypto`)                             |
| Logging      | `log/slog` (stdlib), structured, injected                  |
| Validation   | `go-playground/validator/v10`                              |
| Integration tests | `testcontainers-go` with the MariaDB module           |

`CGO_ENABLED=0` everywhere: the binary is static and cross-compiles to
`linux/arm64` from macOS with no C toolchain. The timezone database is compiled
in via `time/tzdata`, so `time.LoadLocation` works even on a runtime image with
no `/usr/share/zoneinfo` — the streak depends on it.

---

## Architecture

Clean Architecture, package by feature. Each module owns its own layers;
cross-cutting infrastructure lives in `/platform`, cross-cutting
domain/application helpers in `/shared`.

```
cmd/
  api/main.go                 composition root: config, wiring, graceful shutdown
  migrate/main.go             goose up/down/version against the embedded migrations
internal/
  modules/
    auth/                     register, login, logout, /me, activity counters,
                              email verification, password reset
      domain.go               entities, sentinel errors, ports (Repository, Service, ...)
      service.go              use cases, depends only on the ports
      repository_mysql.go     MariaDB implementation
      handler.go              Fiber transport
      routes.go               route registration
      dto.go                  request/response structs + validation tags
    progress/                 read + upsert the user's progress (same layout)
  shared/                     infra-agnostic, reusable
    apperror/                 error type, kinds, HTTP status mapping
    valueobject/              Email, ID
    httpx/                    JSON envelope + error rendering (Fiber-aware)
    pagination/               page/limit parsing + response metadata
    validatorx/               configured validator + custom rules
  platform/                   concrete infrastructure
    mail/                     SMTP and log mailers, message composition
    config/                   typed env configuration
    database/                 *sql.DB setup, pool tuning, health, migrations
    server/                   Fiber builder, middleware wiring, shutdown
    middleware/               request id, recover, access log, CORS, JWT guard
    auth/                     JWT issue/verify, bcrypt hashing
    logger/                   slog construction
  testsupport/                ephemeral MariaDB for integration tests (build tag)
migrations/                   goose .sql files, embedded through embed.FS
cmd/api/adapters.go           the ports where auth and progress meet, wired here
```

**Dependency rules** (enforced by review, and easy to check by grepping imports):

- `handler → service → Repository interface`. Interfaces are declared in the
  module's `domain.go`; implementations point inward.
- Modules may import `/shared` and `/platform`.
- `/shared` imports no module and never `/platform`.
- `/platform` imports no module.
- No module imports another module. Cross-module needs go through interfaces
  wired at the composition root, and there are two of them: the progress
  module declares `ActivityRecorder` (implemented by the auth service) and the
  auth module declares `ProgressSnapshot` for `/me` (implemented by the
  progress service). Neither package imports the other; they meet in
  `cmd/api/adapters.go`.
- Only `/shared/httpx` and `/platform` import Fiber. Domain and service code is
  transport-agnostic; it never sees a `*fiber.Ctx` or a `*sql.DB`.

`context.Context` is propagated from the handler down to
`QueryContext`/`ExecContext`. Repository errors are wrapped with `%w`; domain
sentinels (`ErrUserNotFound`, `ErrInvalidCredentials`, `ErrProgressNotFound`,
…) are mapped to HTTP statuses in the handlers, and database errors never
reach the client.

---

## API

Every route lives under `/api`. `/health` is deliberately outside it: it is
infrastructure, not API surface.

Success responses are the resource itself, unwrapped. Failures are a flat
object:

```jsonc
{ "error": "validation_failed", "message": "…", "fields": [ … ] }   // fields only on 400
```

| Method | Path                 | Auth   | Description                              |
| ------ | -------------------- | ------ | ---------------------------------------- |
| GET    | `/health`            | public | Liveness + MariaDB ping (503 if down)    |
| POST   | `/api/auth/register` | public | Create an account **and open a session** |
| POST   | `/api/auth/login`    | public | Exchange credentials for a 1 h JWT       |
| POST   | `/api/auth/logout`   | public | 204 no-op, see below                     |
| POST   | `/api/auth/verify-email` | public | Confirm an address with the emailed code |
| POST   | `/api/auth/resend-verification` | public | Issue a fresh code, always 204 |
| POST   | `/api/auth/forgot-password` | public | Email a reset link, always 204 |
| POST   | `/api/auth/reset-password` | public | Redeem a reset link |
| GET    | `/api/me`            | Bearer | Identity + progress document in one call |
| PUT    | `/api/profile`       | Bearer | Store the onboarding identity            |
| GET    | `/api/progress`      | Bearer | Read the caller's document               |
| PUT    | `/api/progress`      | Bearer | Create or replace the caller's document  |
| PATCH  | `/api/progress`      | Bearer | Merge the caller's own members           |
| POST   | `/api/progress/answer` | Bearer | Grade one attempt server side          |
| POST   | `/api/progress/lesson-complete` | Bearer | Mark a lesson complete        |

`register` and `login` both return a session:

```jsonc
{
  "token": "eyJhbGciOi…",
  "tokenType": "Bearer",
  "expiresAt": "2026-08-21T22:10:52Z",
  "expiresIn": 3599,
  "user": {
    "id": 1,
    "email": "learner@example.com",
    "createdAt": "2026-08-21T21:10:52Z",
    "lastActiveDate": "2026-08-21",   // UTC calendar day
    "distinctLoginDays": 1,
    "streakDays": 1
  }
}
```

`GET /api/me` returns `{ "user": …, "progress": … }`, where `progress` is
`null` until the first save — it is the single call a client needs at boot.

### The profile

`PUT /api/profile` stores the identity captured during onboarding and returns
the user. It is idempotent, and it does **not** count as activity: filling in a
form is not practising, and letting it move the streak would make the streak
mean something else.

```jsonc
{ "name": "ホルヘ", "gender": "neutral", "birthday": "1990-03-14" }
```

- `name` is trimmed, 1–80 characters, any script — the app is about Japanese,
  so rejecting non-ASCII names would be an own goal — and refuses control
  characters, which otherwise end up in an email header or a heading.
- `gender` is one of `male`, `female`, `neutral`. English tokens like every
  other value here: what the interface shows is a translation the client owns,
  and storing display text would tie the database to whatever language the app
  happened to speak that year. It is a short string rather than a SQL enum, so
  adding an option is a deploy of the client, not a migration.
- `birthday` is optional, `YYYY-MM-DD`, not in the future and not before 1900.
- `timezone` is optional, an IANA name such as `America/Lima`, validated by
  actually loading it rather than against a pattern — the only thing that
  matters is whether this binary can resolve it later. `Local` is refused: it
  resolves to wherever the server happens to be, which would tie one account's
  streak to the box's own clock.

The profile rides **inside** the user (`user.profile`), not beside it, so it
cannot drift out of sync between the four endpoints that return a user:
register, login, verify-email and `/me`. It is `null` until onboarding is
done, which is what tells a client to show the form.

### The progress document

`GET`/`PUT /api/progress` exchange the document **verbatim**: the body of the
`PUT` is the document, and the response is what was stored. The API imposes no
schema on it — it is validated only as *non-empty, valid JSON of at most
256 KiB* — so the client owns its shape and versions it with its own
`schemaVersion` field. (256 KiB fits both syllabaries with per-skill scores
and roughly 5.000 attempts; `HTTP_BODY_LIMIT` is set above it so an oversized
document gets a descriptive 400 rather than a bare 413.)

```bash
curl -X PUT localhost:8080/api/progress \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"schemaVersion":1,"masteryByKana":{"あ":"mastered"},"completedLessonIds":["l1"]}'
```

`PUT` is a full-document upsert (one row per user, `INSERT … ON DUPLICATE KEY
UPDATE`) and therefore last-write-wins. That is correct for one user on one
device; concurrent writers would need an `updatedAt` precondition.

### Patching your own members

`PUT` replaces the whole document, which means a client that keeps state of its
own — a review schedule, say — would have to send everything back and race with
itself. `PATCH` exists for that: it replaces the given **top-level** members and
leaves every other one alone, inside the same locked transaction as the grading
endpoints.

```bash
curl -X PATCH localhost:8080/api/progress \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"srsByKana":{"し":{"box":0,"dueAt":1756150000000}}}'
```

The merge is per key, not deep: sending `srsByKana` swaps the whole map, which
is what makes *removing* an entry expressible at all.

`masteryByKana`, `skillsByKana`, `recentAttempts` and `schemaVersion` are
refused with 400 `protected_field`, and one of them poisons the whole patch —
nothing is written. That is a guard against a client overwriting server-computed
state with a stale copy, **not** a security boundary: `PUT` still replaces
everything, because the guest-to-account migration has to be able to write all
of it.

A patch does not count as activity. It is a background sync of something the
user already did, and the call that did it recorded the day; counting it again
would let a sync extend a streak on its own.

### Server-side grading

Two endpoints change the document instead of replacing it. Both return the
updated document, and both are safe against concurrent writers: the
read-modify-write runs inside a transaction that locks the row with
`SELECT … FOR UPDATE`, so unlike `PUT` nothing is lost.

```bash
curl -X POST localhost:8080/api/progress/answer \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"kana":"あ","skill":"recognition","correct":false}'

curl -X POST localhost:8080/api/progress/lesson-complete \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"lessonId":"l1"}'
```

`answer` moves the score of one skill, recomputes the mastery of that kana and
appends to the attempt window. The rules live in
`internal/modules/progress/mastery.go`:

| Rule | Value |
| ---- | ----- |
| Correct answer | `score + 20`, capped at 100 |
| Wrong answer | `score - 30`, floored at 0 (a mistake costs more than a hit gains) |
| Mastery input | the **average of the four skills**, so one strong skill is not mastery |
| `discovered` / `learning` / `familiar` / `mastered` | average `< 25` / `≥ 25` / `≥ 50` / `≥ 80` |
| `locked` | the kana is absent from the document |
| `recentAttempts` | rolling window, newest 200 — stored, never used in the calculation |

Each attempt is recorded as `{kana, skill, correct, chosen?, at}`. `chosen` is
what the learner picked and is only meaningful on a wrong answer: it is what
turns "failed on し" into "confuses し with つ". It is not validated as a kana,
because a *read this character* question offers romaji.

**Schema versions.** v1 recorded only `{correct, at}`, which cannot answer that
question at all; v2 adds the rest. The server **reads 1 and 2 and always writes
2**, so a v1 document is upgraded in place the first time it is answered or a
lesson is completed. Entries written under v1 are left as they are rather than
rewritten — the kana they lack cannot be invented — and age out on their own as
the 200-entry window turns over. A version outside that range is refused with
409 rather than rewritten under the wrong rules.

Because mastery is the average of four skills, **one skill alone cannot get a
kana past `learning`**: five correct answers on recognition put that score at
its 100 cap, which is an average of 25. Practising all four evenly, from zero
and with no mistakes, it takes 4 correct answers each — 16 in total — to reach
`mastered`.

`kana` must be **one character of the syllabary**: a single kana, or a digraph
ending in a small ya/yu/yo (`きゃ`, `ちょ`), optionally followed by a chōonpu
(`ラー`). A word is rejected with `invalid_answer`, because rune counting alone
cannot tell `きゃ` (one character) from `つき` (two), and accepting the latter
would key the mastery map on something no client ever looks up. A quiz whose
stimulus is a word sends one request per kana it contains.

Mastery is recomputed from the scores every time, so a kana can also fall
back when the learner starts missing it.

`lesson-complete` adds an id to `completedLessonIds`, sorted and without
duplicates, so replaying a lesson is a no-op.

Both endpoints only rewrite the keys they own: the document is decoded member
by member, so any field the client adds later survives a grading call instead
of being dropped. A document whose `schemaVersion` is newer than the server
understands is refused with 409 rather than rewritten with the wrong rules.

**What this duplicates, and what it deliberately does not.** Scoring these
requests server side means the thresholds now live here as well as in the
client, which is the cost of the feature. Two things were kept out of it:
kana are validated by their Unicode blocks (a kana is a kana, digraphs like
`きゃ` included) instead of against a catalogue, and lesson ids are validated
by format only — the list of lessons stays in the client, where the
curriculum is. Neither can go stale when the curriculum grows.

### Who owns what

The split with the web client is deliberate:

- **The client** owns the curriculum: the catalogue of kana, lessons and
  quizzes. It can compute mastery itself and persist the whole document with
  `PUT`, or delegate the scoring to the two grading endpoints above.
- **The API** owns identity and everything that must not be forgeable because
  it depends on the server clock: `lastActiveDate`, `distinctLoginDays` and
  `streakDays`.

Activity is recorded on every login and on every progress write — including
the two grading endpoints — and it is idempotent within a **calendar day**: a
second visit the same day changes nothing, the next day extends the streak, and
a gap resets `streakDays` to 1 while `distinctLoginDays` keeps growing.

The day is cut in the **user's own timezone**, taken from `profile.timezone`,
falling back to UTC for accounts that never sent one. This is not cosmetic: in
the Americas a UTC day turns over in the early evening, so two sessions the
same evening — one at seven, one at nine — counted as two separate days and
inflated both the streak and `distinctLoginDays`.

A stored day that is *ahead* of today is treated as "already active today"
rather than as a gap. That happens when someone flies west or edits their
timezone to one further behind, and treating it as a break would reset a streak
the user did nothing to lose. It grants no free days either, and the stored day
never walks backwards.

The **registration day is always recorded in UTC**, because the profile — and
with it the timezone — arrives afterwards. It self-corrects on the next
activity, at the cost of at most one odd day.

> Editing the timezone can shift the day boundary once, which is enough to
> manufacture a single extra day. It is a bounded, self-inflicted quirk in a
> personal learning app, and the alternative — trusting a zone sent on every
> request — would put the streak back in the client's hands, which is precisely
> what keeping it server-side avoids.

### Email verification

Verification is off by default: registering opens a session straight away and
the code is a nicety. Setting `AUTH_REQUIRE_VERIFIED_EMAIL=true` turns it into
a gate:

```
register        201  {"pendingVerification": true, "user": {…}}   ← no token
login           403  email_not_verified
verify-email    200  {"token": "…", "user": {…, "emailVerified": true}}
login           200  {"token": "…", …}
```

The gate is enforced by **never issuing a credential** before the address is
confirmed, not by a flag the client is trusted to respect. There is
consequently no per-request verification check on the protected routes: a
valid token can only have been issued to a verified account, which is both
stronger and cheaper than checking the database on every call.

What the flow is careful about:

- **Codes and reset tokens are stored hashed** (SHA-256). A dump of the
  database does not let anyone confirm an address or seize an account.
- **A six digit code is only a million possibilities**, so each one dies after
  `AUTH_MAX_VERIFICATION_ATTEMPTS` wrong guesses (5), and the endpoints are
  rate limited on top.
- **One live secret per account per purpose.** Asking for a new code or a new
  link invalidates the previous one and resets the attempt counter.
- **Reset links are single use.** Redeeming one burns it; a link sitting in a
  mail archive stops working. A rejected password does *not* burn it.
- **Nothing leaks which addresses are registered.** `forgot-password` and
  `resend-verification` answer 204 whatever happened, and every way a code can
  fail — wrong, expired, spent, over-guessed, unknown account — returns the
  same error.
- **A completed reset also marks the address verified**: receiving the email
  proved ownership.
- **Verifying an already verified address returns 409, never a token.** The
  code is the only proof of ownership on that endpoint, so handing out a
  session for a merely *known* address would be an authentication bypass.
- **A failed send is never surfaced.** The account still exists and the code
  is stored; the client can ask for another one. Reporting the failure would
  both leak existence and turn a mail outage into a broken signup.

Emails go through `MAIL_DRIVER`. The default, `log`, writes the whole message
to the log instead of sending it — so the signup flow can be exercised on
macOS with no SMTP server anywhere, with the code right there in the terminal.
Set `MAIL_DRIVER=smtp` and the `SMTP_*` variables for the real thing.
Configuration validation refuses `MAIL_DRIVER=log` together with
`APP_ENV=production` and the gate on, because nobody could ever finish
signing up.

### Rate limiting

`RATE_LIMIT_ENABLED=true` by default, with in-memory sliding windows — the
right store for a single instance:

| Endpoint | Default | Keyed by |
| -------- | ------- | -------- |
| `login`, `verify-email`, `reset-password` | 10 / 15 min | IP |
| `register` | 5 / hour | IP |
| `forgot-password`, `resend-verification` | 5 / hour | IP **and destination address** |

The endpoints that send mail are keyed by destination as well: limiting only
by IP would still let one attacker flood a single inbox from many addresses,
and would make a whole household behind one NAT share a budget.

> **Behind a reverse proxy, set `HTTP_TRUSTED_PROXIES`.** Every request
> arrives from the proxy, so without it the per-IP limit silently becomes one
> global limit shared by everybody. `X-Forwarded-For` is only honoured when
> that list is set — trusting it unconditionally would let any caller forge
> its own address and walk straight through.

### Auth notes

- The password must be at least 8 characters, contain at least one letter and
  one digit, and be **at most 72 bytes** — bcrypt's own limit, and the reason
  that bound counts bytes rather than characters. In an app about Japanese the
  difference is not academic: 30 kana are 90 bytes, so a client that validates
  a maximum of 72 *characters* would let a password through that this API
  rejects.
- Access tokens last **1 hour** and there are no refresh tokens by design
  (`JWT_TTL` is an env var if that turns out to be annoying in practice).
  Clients should treat a 401 as "drop the token and show the login screen".
- `POST /api/auth/logout` returns 204 but **revokes nothing**: the token stays
  valid until it expires. Tokens are stateless and there is no denylist, so
  the real logout is the client discarding the token. The endpoint exists to
  keep the client API symmetric and to give revocation a place to land later.

```bash
# Register (already returns the token)
TOKEN=$(curl -sX POST localhost:8080/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"learner@example.com","password":"supersecret1"}' \
  | sed -E 's/.*"token":"([^"]+)".*/\1/')

# Everything the app needs to boot
curl -s localhost:8080/api/me -H "Authorization: Bearer $TOKEN"
```

---

## Run locally on macOS

Docker is only needed for MariaDB. **The API itself runs natively** — the
`GOOS=linux GOARCH=arm64` build exists purely for the Raspberry Pi image.

```bash
# 0. Prerequisites: Go (latest stable) and Docker Desktop
brew install go

# 1. Configuration
cp .env.example .env
# Edit .env and set a real JWT_SECRET (>= 32 chars):
#   openssl rand -base64 48
# Keep DB_HOST=localhost: the API runs on the host, MariaDB in a container.

# 2. Start only the database
make db-up          # docker compose up -d mariadb

# 3. Run the API natively
make dev            # go run ./cmd/api, .env is loaded by the Makefile

# 4. Check it
curl -s localhost:8080/health
```

Migrations are applied automatically at startup from the embedded FS
(`DB_AUTO_MIGRATE=true`). To drive them by hand:

```bash
make migrate-up
make migrate-status
make migrate-down
```

Stop the database with `make db-down`.

---

## Configuration

Every setting comes from environment variables; `.env.example` documents all of
them with their defaults. The two values that matter most:

```ini
# The API runs natively on the host (macOS development):
DB_HOST=localhost

# The API runs inside docker compose (Raspberry Pi deploy):
DB_HOST=mariadb
```

```ini
# Required, at least 32 characters, no default:
JWT_SECRET=…
```

The process refuses to start and prints every problem at once when the
configuration is invalid.

The connection pool defaults are sized for a small box and are all
configurable: `DB_MAX_OPEN_CONNS=10`, `DB_MAX_IDLE_CONNS=5`,
`DB_CONN_MAX_LIFETIME=5m`.

---

## Testing

Two tiers, deliberately separated so the everyday loop stays fast.

### 1. Unit tests — fast, no Docker, no database

Table-driven tests of the service layer against in-memory mock repositories,
plus the JWT/bcrypt primitives. They run anywhere, in milliseconds.

```bash
make test          # go test ./...
make test-race     # go test -race ./...
```

### 2. Integration tests — real MariaDB via testcontainers

Each package spins up an **ephemeral MariaDB container**, applies the same
embedded goose migrations the API runs at startup, exercises the real
`repository_mysql.go` implementations and tears the container down afterwards.
Same engine in dev, test and production.

They are behind the `integration` build tag, so the unit suite above never
compiles testcontainers or needs a daemon.

```bash
make test-integration    # go test -tags=integration ./...
make test-all            # go test -tags=integration -race ./...
```

Requires a running Docker daemon. Without one, the suites print

```
SKIP: integration tests need a running Docker daemon (testcontainers). Start Docker and retry: …
```

and exit successfully — they never hang and never fail the build.

Coverage: user create + fetch-by-email + fetch-by-id + the unique-email
constraint; verification codes and reset tokens (replacement resetting the
attempt counter, single use, cascade delete with the account); the progress `INSERT … ON DUPLICATE KEY UPDATE` round trip
(insert, update in place, single row, read back) and the foreign key rejecting
an unknown owner; that `Mutate` serialises concurrent writers (20 goroutines
racing on one row, none of them lost — the same test fails with 9 lost writes
if the `FOR UPDATE` is removed) and rolls back on error; and every transition
of the activity counters (first day,
same-day no-op, consecutive days, gap reset, month boundary), which is worth
pinning against the real engine because the streak is computed in a single
`UPDATE` whose `SET` list reads `last_active_date` before overwriting it.

---

## Deploy to Raspberry Pi

Target: Raspbian 64-bit (`linux/arm64`), Docker + Docker Compose installed,
data on an external SSD.

```bash
# On the Pi (or build on macOS and push to a registry)
git clone <this repo> japo-backend && cd japo-backend

cp .env.example .env
# Set at least:
#   APP_ENV=production
#   JWT_SECRET=<openssl rand -base64 48>
#   DB_PASSWORD=<something strong>
#   LOG_FORMAT=json
# DB_HOST is overridden to `mariadb` by docker-compose.yml, no need to touch it.

# Put MariaDB's data on the SSD: create the directory,
mkdir -p /mnt/ssd/japo/mariadb
# then uncomment the driver_opts block under `volumes: mariadb_data:` in
# docker-compose.yml and point `device` at it.

docker compose up -d --build
docker compose logs -f api
curl -s localhost:8080/health
```

**Two things to get right the day the web client moves to the Pi**, because
both fail only in production:

- `CORS_ALLOW_ORIGINS` is set to `http://localhost:4200` for development. The
  browser will block the app the moment it is served from any other origin,
  so set it to the origin the client is actually served from. It is a
  comma-separated list, and `*` must not survive into production.
- The client cannot call `http://localhost:8080` from a phone or a laptop:
  `localhost` resolves to the *browser's* machine, not to the Pi. Either point
  it at the Pi's address, or — simpler and what avoids both problems — put a
  reverse proxy in front so the app and the API share one origin (`/api` to
  this service, everything else to the static build). Same origin means no
  CORS configuration at all and no hardcoded addresses.

### Building the image from macOS

```bash
make docker-build        # docker build --platform linux/arm64 -t japo-api:latest .
```

The multi-stage Dockerfile compiles on the build machine's native architecture
and cross-compiles the binary (`CGO_ENABLED=0 GOOS=linux GOARCH=arm64`), so no
QEMU emulation is involved. The runtime stage is Alpine — small, but with a
shell for debugging — with `ca-certificates` and `tzdata`, running as the
non-root user `app` (uid 10001).

Push it to a registry (or `docker save | ssh … docker load`) and run
`docker compose up -d` on the Pi.

Only a static binary is needed at runtime:

```bash
make build-arm64         # bin/api-linux-arm64, ~11 MB, no libc dependency
```

### Memory footprint

The stack is tuned to stay well within a 2 GB Pi:

- `my.cnf` caps MariaDB with `innodb_buffer_pool_size=128M`, small per-connection
  buffers, `performance_schema=OFF` and `max_connections=30`; compose limits the
  container to 512 MB.
- The API pool opens at most 10 connections, Fiber runs with
  `ReduceMemoryUsage` and a 256-connection concurrency cap; compose limits the
  container to 256 MB.
- `innodb_flush_log_at_trx_commit=2` trades up to one second of writes on a
  crash for far less storage wear — acceptable for non-critical data.

### Operations

- `GET /health` pings the database; Docker's `HEALTHCHECK` uses it.
- `SIGINT`/`SIGTERM` trigger a graceful shutdown: Fiber drains in-flight
  requests (`HTTP_SHUTDOWN_TIMEOUT`, 15 s) and then the pool is closed.
- Migrations run on startup, so a `docker compose up -d --build` after a schema
  change is all that is needed.

---

## Troubleshooting

**`docker build` fails with `failed to xattr ... ._something: operation not permitted`.**
The project lives on an exFAT volume, where macOS stores extended attributes in
`._*` sidecar files that the Docker build context cannot read. Clear them and
retry:

```bash
dot_clean -m .
```

They are already ignored by git, by `.dockerignore`, by the `//go:embed 0*.sql`
pattern in `migrations/` and by the `linters.exclusions.paths` rule in
`.golangci.yml` — golangci-lint's loader, unlike the go tool, would otherwise
try to parse `._dto.go` as Go source. `make lint` also runs `dot_clean` first
when it is available.

**`make test-race` fails with `-race requires cgo`.**
The race detector needs a C compiler. On macOS: `xcode-select --install`. The
application itself is always built with `CGO_ENABLED=0`; only the race detector
needs it.

---

## Make targets

```
dev               Run the API natively (macOS), loading .env
build             Build the native binary into bin/
build-arm64       Build the static linux/arm64 binary for the Raspberry Pi
test              Unit tests only: fast, no Docker, no database
test-race         Unit tests with the race detector
test-integration  Repository tests against an ephemeral MariaDB (needs Docker)
test-all          Everything, with the race detector (needs Docker)
lint              Run golangci-lint, or go vet when it is not installed
migrate-up        Apply the embedded goose migrations
migrate-down      Roll back the last goose migration
migrate-status    Print the applied schema version
db-up             Start only MariaDB (development on macOS)
db-down           Stop the compose stack
db-shell          Open a mysql shell in the MariaDB container
docker-build      Build the linux/arm64 deploy image
up                Start the whole stack (API + MariaDB)
logs              Follow the API logs
clean             Remove build artefacts
```

`make` with no target prints this list.
