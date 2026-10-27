# Custom Log Aggregator test documentation

Date: 2026-10-03 (Asia/Kolkata)

## Test layout

New cross-package tests are kept in this directory:

- `platform_pipeline_test.go` — local platform pipeline and cold-store tests.

The project’s existing package-level tests remain under their respective
`internal/*` and `cmd/*` directories.

## Tests added

`platform_pipeline_test.go` adds:

1. A platform pipeline test that publishes a log and metric through the
   ingest buffer, waits for the asynchronous consumer, and verifies both are
   queryable through the platform’s log and metrics data-source adapters.
2. A local cold-store test that verifies filesystem blob round-trip behavior,
   prefix isolation, and safe handling of a missing sanitized path.

## Commands and results

| Command | Result |
| --- | --- |
| `go test ./...` before changes | Passed across all existing packages |
| `go vet ./...` before changes | Passed |
| `go test ./...` after changes | Passed: 193 test executions, 0 failures |
| `go vet ./...` after changes | Passed |
| `web/dashboard: npm run build` | Compiled, then failed TypeScript validation |

The final Go run passed all existing package tests plus the 2 new
cross-package tests. `go test -json` reports 193 passing test executions because
some existing tests also contain subtests.

The dashboard build failure is a pre-existing toolchain/configuration issue:
`web/dashboard/tsconfig.json` sets `ignoreDeprecations` to `"6.0"`, while the
installed dashboard TypeScript version is 5.9.3, which rejects that value. The
Next.js production bundle compiled before this type-check failure.

## External and heavy infrastructure assessment

No Docker Compose stack was started. No database, cache, message broker, cloud
service, Slack endpoint, PagerDuty endpoint, or S3 service was required for the
tests.

The tests use only:

- in-process Go components and `httptest`-style/local fakes already present in
  the repository;
- temporary local directories and files;
- the platform’s local filesystem cold-store fallback.

The Docker Compose file is needed for a multi-process demo: aggregator,
ingest-gateway, alerting-service, and dashboard. Optional S3, Slack, and
PagerDuty integrations would require external credentials/endpoints. Linux
eBPF/CAP_BPF validation is also documented as infrastructure-dependent, but it
was not started or needed here.
