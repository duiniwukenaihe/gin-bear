# Production Hardening Batch 6 Design

## Goal

Add deployable production platform assets so generated applications can be handed to Kubernetes and observability teams with a practical starting point.

## Scope

This batch covers:

- Kubernetes base manifests for Deployment, Service, ConfigMap, HPA, and PDB.
- Prometheus alert rules for availability, readiness, latency, and error rate.
- Documentation linking the deployment assets into the production guide and runbook.
- Docker context hygiene so deploy assets do not enter runtime images.

## Kubernetes Design

The base manifests should be conservative defaults rather than a full platform opinion. Deployment should include:

- Three replicas.
- RollingUpdate strategy.
- Non-root security context.
- Resource requests and limits.
- `/live` liveness probe and `/ready` readiness probe.
- `/metrics` annotations for Prometheus scraping.
- ConfigMap mounted as `application.yaml`.

Service should expose port `8080`. HPA should scale between 3 and 10 replicas based on CPU utilization. PDB should require at least 2 available pods.

## Alerting Design

Prometheus rules should alert on:

- No ready pods.
- High 5xx error rate.
- High p95 latency.
- Readiness endpoint failures.

Rules should use the existing metric names exposed by the framework where possible.

## Testing

Repository tests should verify the important deployment files exist and contain the production-critical fields, without trying to fully lint Kubernetes YAML locally.
