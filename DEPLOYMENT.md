# Deployment Process for Insight Forge

There are two live hosts:

| Host | Org | URL | Use |
|---|---|---|---|
| Sprite `nib-insightforge` | `bill-nib` | https://nib-insightforge-bsmmx.sprites.app/ | Day-to-day / Windmill (current) |
| Fly app `insight-forge` | `nib-235` | https://insight-forge.fly.dev/ | Permanent NIB org machine |

## Fly.io (`nib-235`)

From this repo (after `fly auth login`):

```bash
cd ~/insight-forge
git pull
COMMIT=$(git rev-parse --short HEAD)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fly deploy -a insight-forge --remote-only --yes \
  --build-arg COMMIT="$COMMIT" \
  --build-arg BUILD_TIME="$BUILD_TIME"
```

Verify:

```bash
curl -s https://insight-forge.fly.dev/version
curl -s https://insight-forge.fly.dev/health
```

Secrets live in Fly (`fly secrets list -a insight-forge`), not in git. PartsBase/Serp/UPC came from the sprite dotenv files. ClickHouse + NetBird (`CH_USER`, `CH_PASSWORD`, `NB_SETUP_KEY`) were copied from `fair-market-pricing`. Analyze runs write `nsn_analyses` + `nsn_hits` in ClickHouse Cloud via `fmp-ch-egress`. Screenshots/Tigris stay off.

Keep a single machine: `fly deploy ... --ha=false` (or `fly scale count 1`).

The GitHub Fly integration created the empty app; it could not finish until `fly.toml` + a working Dockerfile landed. Push those files so later GitHub deploys have a config.

---

## Sprite (`nib-insightforge`)

This document exists because of repeated painful failures where code was pushed but old binaries continued serving traffic for hours or days.

## The Only Supported Deployment Method

**From the sprite, you must run:**

```bash
cd /home/sprite/insight-forge || cd ~/insight-forge
git pull
./scripts/reset.sh
```

This is **not optional**. Older methods (manual nohup, legacy reset scripts, building to `/home/sprite/insightforge/bin/...`, etc.) have caused multiple incidents.

## Hard Rules (Non-Negotiable)

1. **Never** start the binary any other way.
2. **Never** skip `./scripts/reset.sh`.
3. The script **will fail the entire deploy** if the live service is not running the exact commit it just built. This is intentional.
4. After any deploy, you must hard-refresh the browser and verify the commit hash shown in the top-right of the UI matches what reset.sh reported.

## What the Hardened reset.sh Now Does

- Aggressively kills every known variant of the old binary (including legacy paths).
- Removes stale binaries from known problem locations.
- Builds the binary with the git commit and build timestamp embedded.
- Runs the full functional gate against the 5 canonical AbilityOne NSNs.
- Starts the new binary.
- **Verifies via `/version`** that the live service is actually serving the commit it just built.
- Only declares success if the version check passes.

If the verification step fails, it kills the new process and exits with an error. This prevents the "we think we deployed but the old code is still live" situation.

## Observability (Added After Repeated Failures)

- `/health` and `/version` endpoints now return `commit` and `buildTime`.
- The UI header shows the live commit hash + build time in the top right.
- The binary prints its commit on startup.

## Verifying the Live Deployment

After a deploy, verify that the running service matches the latest source commit.

### From the sprite (canonical)

```bash
curl http://127.0.0.1:8080/version
curl http://127.0.0.1:8080/health
```

Both return `commit` and `buildTime`. The `commit` value must match `git rev-parse --short origin/main`.

### Public URL auth (Sprites gateway)

Insight Forge itself has **no API key**. Auth (if any) is enforced by the **Sprites URL gateway**, not the Go app.

| Mode | Command | Behavior |
|------|---------|----------|
| `sprite` (default) | `sprite url update --auth sprite -s nib-insightforge` | Org members / Bearer org token only |
| `public` | `sprite url update --auth public -s nib-insightforge` | Anyone with the URL (Windmill, curl, etc.) |

For Windmill / open machine API access, use **public** mode (current preference for integration work).

```bash
sprite url update --auth public -s nib-insightforge
# Verify unauthenticated:
curl -s -X POST https://nib-insightforge-bsmmx.sprites.app/api/analyze \
  -H 'Content-Type: application/json' \
  -d '{"nsn":"7530011245660"}' | head -c 200
```

When auth is `sprite`, anonymous calls get 401/302 to login — that is **not** an Insight Forge bug.

### From a local machine (via the sprite CLI)

If the URL is still in `sprite` auth mode, anonymous `curl` returns an auth **redirect**, not JSON. For automated verification without public auth, run the check inside the sprite:

Instead, run the check inside the sprite from your machine using `sprite exec`:

```bash
sprite exec -s nib-insightforge -- sh -c 'curl -s http://127.0.0.1:8080/version'
```

Then confirm the reported `commit` matches the latest origin/main:

```bash
git rev-parse --short origin/main
```

If the two values match, the latest build is live. If they differ, an old binary is still serving traffic — re-run `./scripts/reset.sh` on the sprite.

> Note: when passing a shell command to `sprite exec`, use `--` before `sh -c '...'` so flags like `-c` are not consumed by the CLI.

## Common Failure Modes We Have Seen (and how the new process addresses them)

| Failure Mode                          | Symptom                              | How New Process Prevents It                          |
|---------------------------------------|--------------------------------------|-----------------------------------------------------|
| Legacy binary in different path       | Old UI + old report text             | Aggressive kill + removal of `/home/sprite/insightforge/bin/insightforge` |
| Stale binary survives `pkill`         | Version mismatch                     | Version verification step that fails the deploy     |
| `git clean -fd` removes `go.sum`      | Build fails with missing modules     | Automatic `go mod tidy` before build                |
| Partial `git pull` / dirty tree       | Mixed old + new code                 | `git reset --hard` + `git clean -fd` inside reset   |
| No way to know what is actually live  | Confusion about which commit is running | Commit shown in UI + `/version` endpoint            |

## If Something Still Goes Wrong

1. Do **not** manually start the binary.
2. Run the diagnostics:
   ```bash
   curl http://127.0.0.1:8080/version
   curl http://127.0.0.1:8080/health
   ps aux | grep -E 'insightforge|insight-forge'
   ls -l /home/sprite/insightforge/bin/insightforge 2>/dev/null
   ```
3. Re-run `./scripts/reset.sh`. It is designed to be re-runnable and self-correcting.

## History Note

This document was created after multiple rounds of "the UI and data don't match what we just pushed." The version verification gate + commit embedding in the UI were added specifically to make that class of failure impossible (or at least immediately obvious).

Last updated: 2026-06
