# NarratoAI Self-Hosted Production Deployment Design

## Goal

Host the NarratoAI frontend and backend on the user's production server and automatically deploy each repository after changes reach `main`. The public application uses one origin, `https://narratoai.cn`, with frontend pages at `/` and backend APIs at `/api/`.

## Architecture

```text
Client HTTPS :443
       |
       v
Production ingress (Nginx or Caddy)
       |-- /      -> narratoai-frontend:80
       |-- /api/  -> narratoai-api:8000
                              |
                              v
                       narratoai-postgres
```

The ingress is the only public application entry point. Ports `8000` and `15432` are removed from public exposure; PostgreSQL remains reachable only through Docker networking or a loopback binding required for administration. The frontend is built with a same-origin API base and calls `/api` rather than a separate public backend origin.

The production Compose project manages ingress, frontend, API, and PostgreSQL. The existing production database volume and environment files remain authoritative and are never recreated by application deployment. Existing payment secrets and certificates remain mounted from server-owned paths.

## Images And Registries

Both repositories publish versioned Docker images to Alibaba Cloud ACR:

- frontend image: static production build served by Nginx;
- backend image: existing Go API release image.

The production environment selects immutable image tags through `FRONTEND_IMAGE` and `BACKEND_IMAGE`. The server is not a Git checkout and does not build application source. Creem compatibility code remains present, while the active Credits purchase experience continues to use WeChat and Alipay QR payments.

## Routing And TLS

`narratoai.cn` already resolves to `1.14.190.112`. The ingress obtains and renews a trusted TLS certificate and redirects HTTP to HTTPS. It preserves the client and scheme headers forwarded to the API, supports WebSocket upgrades, permits the existing large request body size, and uses extended timeouts for long AI workflows.

The frontend supports React Router fallback without caching HTML. Fingerprinted static assets use long immutable caching. Payment callback URLs and the backend public base URL use `https://narratoai.cn/api/...`. Backend CORS allows the production origin and required desktop application origins; ordinary browser traffic is same-origin.

## Automatic Deployment

### Frontend Repository

On a push to `main`, GitHub Actions:

1. installs locked dependencies;
2. runs TypeScript checks, the full test suite, and the production build;
3. builds and pushes a versioned frontend image to ACR;
4. connects to production over SSH;
5. records the current `FRONTEND_IMAGE`, updates it to the new immutable tag, pulls the image, and replaces only the frontend service;
6. verifies the container health, homepage, a static asset, and the proxied API health endpoint;
7. restores the previous tag and frontend container if verification fails.

### Backend Repository

On a push to `main`, GitHub Actions:

1. runs `go test ./...`;
2. builds and pushes a versioned backend image through the existing ACR release mechanism;
3. connects to production over SSH;
4. records the current `BACKEND_IMAGE`, updates it, and replaces only the API service;
5. waits for migrations and verifies container and HTTP health;
6. restores the previous tag if verification fails.

Frontend and backend workflows use independent concurrency groups. A newer deployment cancels an obsolete queued or running deployment for the same repository. Deployments never recreate PostgreSQL.

## Secrets

GitHub Actions Secrets hold ACR credentials and SSH authentication. Repository files contain no server password, private payment key, merchant certificate, database password, or production `.env` content. Production secrets remain in server-owned environment files and mounted certificate directories. Workflow logs must not print secret files or expanded environment values.

SSH key authentication is preferred for automation. If the server currently allows only a password, deployment setup creates a restricted deployment key before enabling automatic delivery.

## Migration Sequence

1. Back up the production Compose configuration and environment files and record current backend image and database volume names.
2. Install the production ingress and frontend service without changing public traffic.
3. Verify locally on the server that frontend routing, login, Credits pages, API proxying, and administration work.
4. obtain the TLS certificate and configure HTTP-to-HTTPS redirection;
5. update backend public URL, CORS, and WeChat/Alipay callback URLs;
6. serve `narratoai.cn` from the self-hosted frontend;
7. verify real-domain login, order creation, callback delivery, settlement, and Credits balance refresh;
8. observe health and logs before detaching the domain from the former frontend provider.

The former provider remains available during the initial observation period so its configuration can be restored quickly if ingress migration fails.

## Failure Handling And Rollback

Deployment scripts use immutable image tags and retain the prior tag before changing a service. Health checks have bounded retries. A failed check restores the former image variable, pulls if necessary, and starts the former container. The workflow reports failure after rollback so the deployment cannot appear successful.

Rollback changes application images and ingress configuration only. It never runs destructive database commands, removes volumes, or silently reverses a completed schema migration. A migration incompatible with the previous backend requires an explicit backward-compatible rollout plan before release.

## Security And Operations

- Expose only TCP ports 80 and 443 for the application.
- Bind PostgreSQL to Docker networking or loopback; do not publish it to the internet.
- Keep the API behind ingress rather than publishing port 8000 publicly.
- Apply request-size and long-running request timeouts compatible with video workflows.
- Add standard security response headers without breaking required media or API behavior.
- Use bounded JSON log rotation for every container.
- Configure restart policies and health checks so services recover after a server reboot.
- Preserve current server-managed payment keys, WeChat certificates, desktop update files, and database volume mounts.

Initial monitoring remains lightweight: Docker health state, deployment workflow results, HTTP smoke checks, and rotated container logs. A separate monitoring platform is outside this scope.

## Verification

The release is accepted when:

- `https://narratoai.cn/` serves the frontend and direct refreshes of application routes do not return 404;
- login, registration, project pages, Credits store, and administration load successfully;
- browser API requests use `https://narratoai.cn/api/` without CORS or mixed-content errors;
- ingress, frontend, API, and PostgreSQL report healthy;
- CNY Credits packages and 30-day validity display correctly;
- both WeChat and Alipay create real QR orders;
- signed callbacks reach the API over HTTPS and duplicate callbacks remain idempotent;
- frontend-only changes replace only the frontend service;
- backend-only changes replace only the API service;
- failed deployment checks restore the previous image;
- a server reboot restores every service without manual intervention.

## Scope Boundaries

This design covers self-hosting, same-origin routing, TLS, automatic image deployment, health verification, and image rollback. It does not introduce Kubernetes, a new observability platform, database replication, multi-server high availability, or automatic rollback of database schemas.
