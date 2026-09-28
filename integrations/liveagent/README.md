# LiveAgent companion delivery

The K-brain integration spans two independent Git repositories. This directory ships the complete LiveAgent change as an applyable Git patch so a K-brain-only checkout still contains the frontend implementation and its regression tests.

- LiveAgent base: `e63588a1328be66530353ad6d274db14b5f0e68d`
- LiveAgent integration commit: `cc594a60f89f51d737cd132efb3d59e64d3266ec`
- Patch: [`kbrain-backend.patch`](kbrain-backend.patch)
- Complete repository-qualified changed-file manifest: [`../../CHANGED_FILES.md`](../../CHANGED_FILES.md)

The patch includes 126 LiveAgent files: the canonical HTTP/SSE client, production chat entry routing, history mutations and recovery, model settings, auxiliary text generation, host capability boundaries, shared UI and regression tests. It is exported directly from the companion commit rather than maintained as a second implementation.

## Apply to a clean LiveAgent checkout

From a LiveAgent checkout at the base commit, with `KBRAIN_REPO` pointing to the K-brain checkout:

```sh
git apply --check "$KBRAIN_REPO/integrations/liveagent/kbrain-backend.patch"
git am "$KBRAIN_REPO/integrations/liveagent/kbrain-backend.patch"
```

A checkout already containing the integration commit needs no patch application.

## Verify the shipped frontend

From the LiveAgent repository:

```sh
npm exec --yes --package=pnpm@10.32.1 -- pnpm --filter liveagent test:frontend
npm exec --yes --package=pnpm@10.32.1 -- pnpm --filter liveagent typecheck
VITE_KBRAIN_BACKEND=true VITE_KBRAIN_URL=http://127.0.0.1:47321 \
  npm exec --yes --package=pnpm@10.32.1 -- pnpm --filter liveagent build
npm exec --yes --package=pnpm@10.32.1 -- pnpm --filter liveagent lint
```

The delivery verification transcript is named `liveagent-frontend-tests.log` and contains the production-path contract tests, complete frontend suite, explicit typecheck, K-brain-mode build and lint, with command headers and exit codes. Verification evidence is kept outside the product repositories. Backend protocol and deployment boundaries are documented in [`../../docs/liveagent-backend.md`](../../docs/liveagent-backend.md).
