# Deploying Insight Forge

Insight Forge runs on its own dedicated sprite: `nib-insightforge`  
Public URL: https://nib-insightforge-bsmmx.sprites.app/

## Current Deployment Method

**Always use `./scripts/reset.sh`** from the `insight-forge` directory on the sprite.

This script:
- Performs a hard git reset to the latest main
- Removes stale binaries
- Builds the binary with the commit hash embedded via ldflags
- Runs functional gates against real AbilityOne NSNs
- Verifies via `/version` that the running service matches the expected commit
- Only starts the binary if verification passes

See `DEPLOYMENT.md` for full details and failure mode history.

## No Subpath Proxying Required

Unlike older shared-sprite setups, Insight Forge now has its own dedicated public URL. There is no need to route through another app's Next.js rewrites or proxy layer.

## Environment

- Port: 8080 (default)
- The UI is a single static file served by the Go binary
- All heavy lifting happens in the Go synthesis engine

## Verification After Deploy

After running `reset.sh`, you should see the new commit hash in:
- The top-right of the UI
- `curl http://localhost:8080/version`

Hard-refresh the browser (⌘ + Shift + R) to ensure you have the latest static assets.
