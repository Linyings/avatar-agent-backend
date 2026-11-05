# NarratoAI Self-Hosted Production Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve the frontend and backend from `https://narratoai.cn` on `1.14.190.112` and automatically deploy versioned ACR images after each repository's `main` branch passes its tests.

**Architecture:** Caddy terminates TLS and routes `/api/*` to the Go API and all other paths to the frontend Nginx container. GitHub Actions independently builds immutable frontend and backend images, updates only the affected image variable over SSH, verifies health, and restores the previous tag on failure.

**Tech Stack:** Docker Compose, Caddy 2, Nginx 1.27, React/Vite, Go 1.26.2, GitHub Actions, Alibaba Cloud ACR, SSH, PostgreSQL 17.

## Global Constraints

- Public application origin is exactly `https://narratoai.cn`.
- Only ports `80` and `443` are public application ports; API `8000` and PostgreSQL `15432` must not be internet-facing.
- Production server remains image-based and is not converted into a Git checkout.
- Frontend and backend deploy independently and never recreate PostgreSQL.
- Every deployment records the previous immutable image tag and rolls back the changed service on failed health checks.
- Database volumes, production `.env`, Alipay keys, WeChat certificates, and server credentials are never committed or printed.
- Existing provider-hosted frontend remains available until real-domain acceptance passes.

---

### Task 1: Make The Frontend Image Production-Network Neutral

**Repository:** `NarratoAI-SaaS-F`

**Files:**
- Modify: `nginx.conf`
- Modify: `src/deploymentConfig.test.ts`
- Modify: `Dockerfile`

**Interfaces:**
- Consumes: frontend requests use a blank `VITE_API_BASE_URL`, producing same-origin `/api/...` calls.
- Produces: a static frontend image that serves SPA routes and does not proxy `/api` to `host.docker.internal`.

- [ ] **Step 1: Add failing deployment assertions**

Extend `src/deploymentConfig.test.ts` with:

```ts
it('keeps backend API routing out of the frontend container', () => {
  const nginx = readFileSync(resolve(process.cwd(), 'nginx.conf'), 'utf8')
  expect(nginx).not.toContain('host.docker.internal')
  expect(nginx).not.toMatch(/location\s+\/api\/\s*\{/)
  expect(nginx).toContain('location /api/fish-audio-proxy/')
  expect(nginx).toContain('try_files $uri $uri/ /index.html;')
  expect(nginx).toContain('Cache-Control "public, immutable"')
})

it('builds production with the same-origin API base', () => {
  const dockerfile = readFileSync(resolve(process.cwd(), 'Dockerfile'), 'utf8')
  expect(dockerfile).toContain('ARG VITE_API_BASE_URL=')
  expect(dockerfile).not.toContain('VITE_TOS_ACCESS_KEY_SECRET')
})
```

- [ ] **Step 2: Verify the tests fail**

Run: `npm test -- --run src/deploymentConfig.test.ts`

Expected: FAIL because `nginx.conf` contains `host.docker.internal`, and the Dockerfile accepts a client-side secret argument.

- [ ] **Step 3: Remove the API proxy and client-side secret build arguments**

Delete only the active generic `location /api/` backend block from `nginx.conf`. Preserve the existing `location /api/fish-audio-proxy/` route, SPA fallback, static caching, upload size, health endpoint, and security headers. In `Dockerfile`, use:

```dockerfile
ARG VITE_API_BASE_URL=
ARG VITE_R2_BASE_URL=https://r2.narratoai.cn
ARG VITE_TOS_UPLOAD_API_BASE_URL=

ENV VITE_API_BASE_URL=${VITE_API_BASE_URL}
ENV VITE_R2_BASE_URL=${VITE_R2_BASE_URL}
ENV VITE_TOS_UPLOAD_API_BASE_URL=${VITE_TOS_UPLOAD_API_BASE_URL}
```

Do not place TOS access keys in browser builds.

- [ ] **Step 4: Verify frontend behavior**

Run:

```bash
npm test -- --run src/deploymentConfig.test.ts
npm test -- --run
npm run build
```

Expected: all frontend tests pass and Vite creates `dist/`.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile nginx.conf src/deploymentConfig.test.ts
git commit -m "fix: make frontend image production network neutral"
```

---

### Task 2: Add The Production Compose And Caddy Ingress

**Repository:** `NarratoAI-SaaS-B-Go`

**Files:**
- Create: `deploy/production/docker-compose.yml`
- Create: `deploy/production/Caddyfile`
- Create: `deploy/production/env.example`
- Create: `deploy/production/config_test.go`

**Interfaces:**
- Consumes: `FRONTEND_IMAGE`, `BACKEND_IMAGE`, existing `/root/narratoai-backend-go/.env`, `wechat_certs`, desktop update mount, and `postgres_data`.
- Produces: services `ingress`, `frontend`, `api`, and `postgres`, with health endpoints reachable inside the Compose network.

- [ ] **Step 1: Write a failing artifact test**

Create `deploy/production/config_test.go`:

```go
package production

import (
    "os"
    "strings"
    "testing"
)

func TestProductionIngressAndPortPolicy(t *testing.T) {
    composeBytes, err := os.ReadFile("docker-compose.yml")
    if err != nil { t.Fatal(err) }
    caddyBytes, err := os.ReadFile("Caddyfile")
    if err != nil { t.Fatal(err) }
    compose, caddy := string(composeBytes), string(caddyBytes)
    for _, required := range []string{"FRONTEND_IMAGE", "BACKEND_IMAGE", "127.0.0.1:15432:5432", "caddy_data", "postgres_data"} {
        if !strings.Contains(compose, required) { t.Fatalf("compose missing %q", required) }
    }
    if strings.Contains(compose, `"8000:8000"`) { t.Fatal("API port must not be public") }
    for _, required := range []string{"narratoai.cn", "handle /api/health", "rewrite * /health", "handle /api/*", "reverse_proxy api:8000", "reverse_proxy frontend:80"} {
        if !strings.Contains(caddy, required) { t.Fatalf("Caddyfile missing %q", required) }
    }
}
```

- [ ] **Step 2: Verify the artifact test fails**

Run: `go test ./deploy/production -count=1`

Expected: FAIL because the production artifacts do not exist.

- [ ] **Step 3: Create the production Compose file**

Use named services and a private network. Key contracts:

```yaml
services:
  ingress:
    image: caddy:2.10-alpine
    restart: unless-stopped
    ports: ["80:80", "443:443", "443:443/udp"]
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    depends_on:
      frontend: { condition: service_healthy }
      api: { condition: service_healthy }

  frontend:
    image: ${FRONTEND_IMAGE:?FRONTEND_IMAGE is required}
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--tries=1", "--spider", "http://127.0.0.1/health"]

  api:
    image: ${BACKEND_IMAGE:?BACKEND_IMAGE is required}
    restart: unless-stopped
    env_file: [.env.production]
    environment:
      HOST: 0.0.0.0
      PORT: 8000
      DATABASE_URL: postgres://narratoai:${POSTGRES_PASSWORD}@postgres:5432/narratoai?sslmode=disable
      PUBLIC_API_BASE_URL: https://narratoai.cn
      FRONTEND_BASE_URL: https://narratoai.cn
      CORS_ORIGINS: https://narratoai.cn,app://narratoai

  postgres:
    image: postgres:17-alpine
    restart: unless-stopped
    ports: ["127.0.0.1:15432:5432"]
    volumes: ["postgres_data:/var/lib/postgresql/data"]

volumes:
  postgres_data: { external: true, name: narratoai_postgres_data }
  caddy_data:
  caddy_config:
```

Preserve the existing API certificate and desktop-update mounts and current PostgreSQL tuning values from the server Compose file.

- [ ] **Step 4: Create the Caddy routing policy**

Create `deploy/production/Caddyfile`:

```caddyfile
narratoai.cn {
    encode zstd gzip

    request_body {
        max_size 500MB
    }

    handle /api/health {
        rewrite * /health
        reverse_proxy api:8000
    }

    # This specialized proxy is implemented by the frontend Nginx image.
    handle /api/fish-audio-proxy/* {
        reverse_proxy frontend:80
    }

    handle /api/* {
        reverse_proxy api:8000 {
            transport http {
                dial_timeout 15s
                response_header_timeout 10m
            }
        }
    }

    handle {
        reverse_proxy frontend:80
    }

    header {
        X-Content-Type-Options nosniff
        Referrer-Policy strict-origin-when-cross-origin
        -Server
    }
}
```

- [ ] **Step 5: Validate Compose and tests**

Run:

```bash
go test ./deploy/production -count=1
POSTGRES_PASSWORD=test FRONTEND_IMAGE=frontend:test BACKEND_IMAGE=backend:test \
  docker compose -f deploy/production/docker-compose.yml config --quiet
```

Expected: tests pass and Compose exits zero without unresolved variables.

- [ ] **Step 6: Commit**

```bash
git add deploy/production
git commit -m "feat: add self-hosted production stack"
```

---

### Task 3: Add An Atomic Service Deployment Script

**Repository:** `NarratoAI-SaaS-B-Go`

**Files:**
- Create: `deploy/production/deploy-service.sh`
- Create: `deploy/production/deploy-service-test.sh`

**Interfaces:**
- Consumes: `deploy-service.sh <frontend|api> <full-image-reference>`, `/root/narratoai/.env`, and `/root/narratoai/docker-compose.yml`.
- Produces: zero exit after verified deployment; nonzero exit after restoring the former image.

- [ ] **Step 1: Write the shell contract test**

Create `deploy-service-test.sh` using stub `docker` and `curl` executables in a temporary `PATH`. Assert:

```bash
./deploy-service.sh unknown image:test
# exits 2

./deploy-service.sh frontend registry.example/frontend:sha-new
# writes FRONTEND_IMAGE=registry.example/frontend:sha-new, calls compose up -d --no-deps frontend,
# checks https://narratoai.cn/health, and exits 0

DEPLOY_TEST_HEALTH_FAIL=1 ./deploy-service.sh api registry.example/api:sha-bad
# restores the previous BACKEND_IMAGE, calls compose up for api again, and exits nonzero
```

- [ ] **Step 2: Verify the test fails**

Run: `bash deploy/production/deploy-service-test.sh`

Expected: FAIL because `deploy-service.sh` does not exist.

- [ ] **Step 3: Implement the deployment script**

The script must:

```bash
set -euo pipefail
service="${1:-}"
image="${2:-}"
case "$service" in
  frontend) image_key=FRONTEND_IMAGE; health_url=https://narratoai.cn/health ;;
  api) image_key=BACKEND_IMAGE; health_url=https://narratoai.cn/api/health ;;
  *) echo "usage: $0 <frontend|api> <image>" >&2; exit 2 ;;
esac
```

Validate the full ACR reference, lock deployments with `flock`, copy `.env` to a timestamped backup, update only the selected key without evaluating the file, run `docker compose pull "$service"` and `docker compose up -d --no-deps "$service"`, then poll container health and the public URL. On any post-update failure, restore the backup and start the former service image. Keep the latest five environment backups.

- [ ] **Step 4: Run shell and syntax tests**

Run:

```bash
bash -n deploy/production/deploy-service.sh
bash deploy/production/deploy-service-test.sh
```

Expected: both exit zero.

- [ ] **Step 5: Commit**

```bash
git add deploy/production/deploy-service.sh deploy/production/deploy-service-test.sh
git commit -m "feat: add rollback-safe production deploy script"
```

---

### Task 4: Publish And Deploy The Frontend On Main

**Repository:** `NarratoAI-SaaS-F`

**Files:**
- Create: `.github/workflows/release-acr.yml`
- Modify: `src/deploymentConfig.test.ts`

**Interfaces:**
- Consumes: ACR secrets, `PRODUCTION_SSH_PRIVATE_KEY`, `PRODUCTION_SSH_HOST`, and `PRODUCTION_SSH_USER`.
- Produces: `${ALIYUN_REGISTRY}/${ALIYUN_REGISTRY_NAMESPACE}/narratoai-frontend:sha-<7-char-sha>` and invokes the server deploy script for `frontend`.

- [ ] **Step 1: Add workflow structure assertions**

Extend the deployment test to read `.github/workflows/release-acr.yml` and assert it contains:

```ts
expect(workflow).toContain('branches: [main]')
expect(workflow).toContain('npm test -- --run')
expect(workflow).toContain('npm run build')
expect(workflow).toContain('docker/build-push-action@v7')
expect(workflow).toContain('deploy-service.sh frontend')
expect(workflow).toContain('concurrency:')
```

- [ ] **Step 2: Verify the test fails**

Run: `npm test -- --run src/deploymentConfig.test.ts`

Expected: FAIL because the workflow does not exist.

- [ ] **Step 3: Create the frontend workflow**

Use `on.push.branches: [main]` and `workflow_dispatch`. Add top-level:

```yaml
concurrency:
  group: production-frontend
  cancel-in-progress: true
```

The test job runs `npm ci`, `npm test -- --run`, and `npm run build`. The image job logs into ACR and publishes `sha-${short_sha}` plus `latest`, with build args `VITE_API_BASE_URL=` and the public R2 URL. The deploy job uses `webfactory/ssh-agent` or an equivalent pinned SSH action, adds the host key with `ssh-keyscan`, logs into ACR on the server without echoing the password, and executes:

```bash
/root/narratoai/deploy-service.sh frontend \
  "$ALIYUN_REGISTRY/$ALIYUN_REGISTRY_NAMESPACE/narratoai-frontend:sha-$SHORT_SHA"
```

- [ ] **Step 4: Validate workflow and frontend**

Run:

```bash
npm test -- --run src/deploymentConfig.test.ts
npm test -- --run
npm run build
```

Expected: all commands pass.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/release-acr.yml src/deploymentConfig.test.ts
git commit -m "ci: deploy frontend image from main"
```

---

### Task 5: Extend Backend Releases With Automatic Deployment

**Repository:** `NarratoAI-SaaS-B-Go`

**Files:**
- Modify: `.github/workflows/release-acr.yml`
- Create: `.github/workflows/release_acr_test.go`

**Interfaces:**
- Consumes: the existing backend ACR build job and the same production SSH secrets.
- Produces: backend `sha-<7-char-sha>` images on `main` and invokes `deploy-service.sh api` only after the full Go suite passes.

- [ ] **Step 1: Write workflow policy tests**

Create a Go test that reads the workflow and checks for `branches: [main]`, `go test ./...`, a deploy job requiring `build-and-push`, `deploy-service.sh api`, and concurrency group `production-backend`.

- [ ] **Step 2: Verify the test fails**

Run: `go test ./.github/workflows -count=1`

Expected: FAIL because the existing workflow publishes tags but has no `main` deploy job.

- [ ] **Step 3: Extend the workflow**

Preserve tag and manual publishing. Add `push.branches: [main]`, top-level backend deployment concurrency, and a deploy job gated to `refs/heads/main`. The deploy command is:

```bash
/root/narratoai/deploy-service.sh api \
  "$ALIYUN_REGISTRY/$ALIYUN_REGISTRY_NAMESPACE/narratoai-backend-go:sha-$SHORT_SHA"
```

Do not run `git pull` on the server. Do not restart `postgres`, `frontend`, or `ingress` for backend-only delivery.

- [ ] **Step 4: Verify backend workflow and code**

Run:

```bash
go test ./.github/workflows -count=1
go test ./... -count=1
```

Expected: all packages pass.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/release-acr.yml .github/workflows/release_acr_test.go
git commit -m "ci: deploy backend image from main"
```

---

### Task 6: Bootstrap The Production Server Without Losing Data

**Files installed on server:**
- `/root/narratoai/docker-compose.yml`
- `/root/narratoai/Caddyfile`
- `/root/narratoai/deploy-service.sh`
- `/root/narratoai/.env`
- `/root/narratoai/.env.production`

**Interfaces:**
- Consumes: current `/root/narratoai-backend-go`, its environment files, image tag, `wechat_certs`, desktop update directory, and PostgreSQL volume.
- Produces: a unified stack reachable on server loopback before public cutover.

- [ ] **Step 1: Record and back up current state**

Over SSH run read-only inspection first:

```bash
cd /root/narratoai-backend-go
docker compose ps
docker inspect narratoai-postgres --format '{{json .Mounts}}'
docker inspect narratoai-api --format '{{.Config.Image}}'
ss -lntp
```

Then create a timestamped backup directory and copy Compose and environment files into it. Do not copy secret contents into command output.

- [ ] **Step 2: Create a restricted deployment SSH key**

Generate a dedicated Ed25519 key for GitHub Actions, add only its public key to the production user's `authorized_keys`, and store the private key in both repositories as `PRODUCTION_SSH_PRIVATE_KEY`. Store host and user as `PRODUCTION_SSH_HOST=1.14.190.112` and `PRODUCTION_SSH_USER=root` unless a dedicated deployment user is configured.

- [ ] **Step 3: Install production artifacts**

Upload the reviewed files from `deploy/production/` into `/root/narratoai`. Set `deploy-service.sh` mode to `0750`. Create `.env` containing the current immutable backend image, the new frontend image, and the existing PostgreSQL password without printing values.

- [ ] **Step 4: Preserve and attach the existing database volume**

Set `narratoai_postgres_data` to the exact volume reported in Step 1. Validate:

```bash
cd /root/narratoai
docker compose config --quiet
docker volume inspect "$EXISTING_POSTGRES_VOLUME"
```

Expected: Compose validation passes and the existing volume exists.

- [ ] **Step 5: Start privately and verify**

Before changing DNS or public proxy configuration, start PostgreSQL, API, and frontend and verify from the server:

```bash
docker compose up -d postgres api frontend
docker compose ps
curl -fsS http://frontend/health
curl -fsS http://api:8000/health
```

Use `docker compose exec` if service names are not resolvable from the host. Confirm login and package APIs against the existing database.

- [ ] **Step 6: Start ingress and verify TLS**

Ensure ports 80/443 are free, then run `docker compose up -d ingress`. Verify:

```bash
curl -fsS https://narratoai.cn/health
curl -fsS https://narratoai.cn/api/health
curl -fsS https://narratoai.cn/api/v1/credit-packages/packages
```

Expected: all return HTTP 200 with the existing production data.

---

### Task 7: Configure Secrets, Payment URLs, And End-To-End Acceptance

**Repositories:** both GitHub repositories and server environment only.

**Interfaces:**
- Consumes: validated production stack and provider credentials already present on the server.
- Produces: fully automatic independent deployments and verified same-origin payments.

- [ ] **Step 1: Configure repository secrets and variables**

Set, without logging values:

```text
ALIYUN_REGISTRY
ALIYUN_REGISTRY_NAMESPACE
ALIYUN_REGISTRY_USERNAME
ALIYUN_REGISTRY_PASSWORD
PRODUCTION_SSH_PRIVATE_KEY
PRODUCTION_SSH_HOST
PRODUCTION_SSH_USER
```

Set image repository variables to `narratoai-frontend` and `narratoai-backend-go` respectively.

- [ ] **Step 2: Update production public URLs**

Set server environment values:

```env
PUBLIC_API_BASE_URL=https://narratoai.cn
FRONTEND_BASE_URL=https://narratoai.cn
FRONTEND_BASE_URLS=https://narratoai.cn
CORS_ORIGINS=https://narratoai.cn,app://narratoai
WECHAT_NOTIFY_URL=https://narratoai.cn/api/v1/credit-payments/wechat/webhook
ALIPAY_NOTIFY_URL=https://narratoai.cn/api/v1/credit-payments/alipay/webhook
```

Restart only API after validating the environment file.

- [ ] **Step 3: Run browser and payment smoke tests**

Verify:

```text
https://narratoai.cn/
https://narratoai.cn/login
https://narratoai.cn/subscription
https://narratoai.cn/admin
https://narratoai.cn/api/health
```

Log in with a non-production-test user, confirm CNY package and expiry display, create one unpaid WeChat QR order and one unpaid Alipay QR order, and confirm both remain pending without granting Credits. Then make one explicitly authorized low-value real payment and verify exactly one Credit lot and one ledger entry.

- [ ] **Step 4: Prove independent automatic deployment**

Merge a harmless frontend version-label change to frontend `main`. Confirm only `frontend` gets a new container ID. Merge a harmless backend build metadata change to backend `main`. Confirm only `api` gets a new container ID. In both runs, verify the workflow uses a `sha-*` image and public health remains 200.

- [ ] **Step 5: Prove rollback**

Use workflow dispatch with a deliberately nonexistent test image in a controlled maintenance window. Confirm `deploy-service.sh` restores the former environment file and container image, returns nonzero, leaves PostgreSQL untouched, and public health returns 200 after rollback.

- [ ] **Step 6: Complete cutover**

Observe container health, payment callback logs, and error rates through the agreed initial observation period. Only after acceptance passes, remove the domain binding from the previous frontend provider. Keep its project configuration intact until the next normal release succeeds.

- [ ] **Step 7: Record production state**

Update the operational deployment notes with the production directory, service names, ACR repositories, public health URLs, rollback command, and observed image tags. Do not record credentials or secret values.
