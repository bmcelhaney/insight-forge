# Deployment Process for nib-insightforge

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

Last updated: 2026-05
