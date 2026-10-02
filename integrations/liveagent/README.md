# LiveAgent companion delivery

The K-brain integration spans two independent Git repositories. This directory ships the complete LiveAgent change as an applyable Git patch so a K-brain-only checkout still contains the frontend implementation and its regression tests.

- LiveAgent base: `e63588a1328be66530353ad6d274db14b5f0e68d`
- LiveAgent current HEAD: `cc594a60f89f51d737cd132efb3d59e64d3266ec`; the patch also includes the subsequent local working-tree changes, including the current Gateway queue recovery, settings wire regression, provider discovery fixture, and unified-model test updates.
- Patch: [`kbrain-backend.patch`](kbrain-backend.patch)
- Complete repository-qualified changed-file manifest: [`../../CHANGED_FILES.md`](../../CHANGED_FILES.md)

The current patch snapshot includes 508 LiveAgent paths and reconstructs tree `73e40fb3b034886789452401890c2e28cee01f3f`. Its SHA-256 is `417a687a7502234ea11622505ec71001a35ac7d7a83119b31aeba628669f243f`. It is exported directly from the companion working tree rather than maintained as a second implementation. The current snapshot includes Gateway queue canonical-identity, stale-ID isolation, restart-cancellation regressions, reconnect lifecycle cleanup, and the symlink-aware Gateway-local workspace activity owner and browser routing; the matching raw evidence is kept in the goal scratch directory, including `browser-queue-result-final.json`.

## Apply to a clean LiveAgent checkout

From a LiveAgent checkout at the base commit, with `KBRAIN_REPO` pointing to the K-brain checkout:

```sh
git apply --check "$KBRAIN_REPO/integrations/liveagent/kbrain-backend.patch"
git apply "$KBRAIN_REPO/integrations/liveagent/kbrain-backend.patch"
```

This is a raw working-tree diff. Apply it to the stated base; a checkout containing the earlier integration commit still needs the subsequent changes, so it must be compared against the captured tree before using this patch.

## Verify the shipped frontend

From the LiveAgent repository:

```sh
npm exec --yes --package=pnpm@10.32.1 -- pnpm test:gui
npm exec --yes --package=pnpm@10.32.1 -- pnpm test:webui
npm exec --yes --package=pnpm@10.32.1 -- pnpm typecheck:gui
npm exec --yes --package=pnpm@10.32.1 -- pnpm typecheck:ui
npm exec --yes --package=pnpm@10.32.1 -- pnpm typecheck:webui
npm exec --yes --package=pnpm@10.32.1 -- pnpm build:gui
npm exec --yes --package=pnpm@10.32.1 -- pnpm build:webui
npm exec --yes --package=pnpm@10.32.1 -- pnpm lint
npm exec --yes --package=pnpm@10.32.1 -- pnpm check:script-tests
```

The final scratch transcripts are `gui-final-closure.log` (2838 passed, 0 failed, 0 skipped) and `webui-workspace-fix.log` (759 passed, 0 failed, 0 skipped). The real K-brain Skills/Memory HTTP round-trip passes all four tests in `resource-real-roundtrip-correct.log`. Run this resource test with `KBRAIN_RESOURCE_CONNECTION_FILE` pointing to an authenticated local backend connection JSON; the full GUI suite requires the same connection for its real round-trip. Earlier `frontend-tests.log` counts are historical. These test results establish the exercised scope; typecheck, build, lint, packaging and signed distribution require their own results. Additional verification evidence is kept outside the product repositories. Backend protocol and deployment boundaries are documented in [`../../docs/liveagent-backend.md`](../../docs/liveagent-backend.md).

Final delivery evidence resides in `/var/folders/s0/8xn72j6s20v1thgbz1__2sfw0000gn/T/grok-goal-b330cd3185db/implementer`. `delivery-closure.json` records the captured patch/tree and evidence hashes. `workspace-protocol-browser-proof.json` and `workspace-activity-mobile-protocol.json` preserve real Chrome workspace subscribe/ACK/activity frames. Gateway-local activity for an offline requested agent is implemented; native Git/project tools, terminal/SSH/SFTP/tunnel ownership and cross-host recovery remain partial or missing.
