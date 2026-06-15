# Contributing to HistorySync Cloud Server

Thanks for your interest in contributing to HistorySync Cloud Server, the Community Edition backend for HistorySync.

This repository is the CE base that the Enterprise server builds on top of. Please keep contributions focused on the shared self-hosted core unless a maintainer explicitly asks for a broader change.

## Before You Start

- Read the project overview and architecture notes in [AGENTS.md](./AGENTS.md) and [docs/server-design.md](./docs/server-design.md).
- Search for existing issues, pull requests, and docs before starting a new implementation.
- For larger changes, public API changes, auth changes, quota or billing semantics, or data model changes, open an issue or discuss the approach with maintainers first.

## Development Setup

Run commands from the repository root.

```bash
go mod download
go mod tidy
make run
```

Useful commands:

```bash
# Build
make build

# Fast unit test tier
make test

# DB-backed integration tier (requires Docker)
make test-integration

# Lint (requires golangci-lint)
make lint

# Run without Make
go run ./cmd/hsync-server
go test -count=1 -timeout 60s ./...
```

## Project Expectations

- Keep changes small and directly related to the problem being solved.
- Follow the existing handler/service/repository layering.
- Keep CE generic and self-hostable. Do not add Enterprise-only commercial policy directly to CE.
- Do not introduce code that inspects or transforms encrypted bundle contents unless the change is explicitly requested.
- Prefer existing helpers, interfaces, and patterns over new abstractions.
- Use ASCII in code and comments.
- Write comments in English and only when they clarify non-obvious behavior.

## Code Layout

Important directories:

- `cmd/hsync-server/`: server entrypoint
- `pkg/handler/`: HTTP handlers and route registration
- `pkg/service/`: business logic
- `pkg/repository/`: PostgreSQL and Redis persistence
- `pkg/provider/`: extension seams and CE default implementations
- `pkg/storage/`: blob storage
- `pkg/ws/`: WebSocket hub
- `migrations/`: SQL migrations
- `docs/`: product, API, and operations documentation

## Tests and Validation

Please validate changes with the smallest relevant check first, then broaden when needed.

- Unit tier: `make test`
- Integration tier: `make test-integration`
- Build check: `make build`
- Lint: `make lint`

Testing notes:

- The default unit tier must stay fast and hermetic.
- Integration tests use the `integration` build tag and require a running Docker daemon.
- The race-enabled test command may fail on some Windows setups without a working CGO toolchain. In that case, run tests without `-race` and mention the limitation in the pull request.
- If your change affects shared behavior, add or update tests where practical.

## Pull Request Guidelines

- Keep one logical change per pull request.
- Include a clear description of the problem, the approach, and any trade-offs.
- Mention the validation you ran.
- Call out follow-up work or known limitations when they matter.
- Do not mix unrelated refactors with behavioral changes.

If your change touches both CE and Enterprise code, keep the CE and EE work in separate pull requests or separate commits, with CE landing first.

## API, Schema, and Product Boundaries

Please discuss with maintainers before changing:

- public API contracts
- authentication flows
- quota semantics
- billing-related behavior
- migration strategy or schema compatibility boundaries

When code and docs disagree, maintainers may treat the current code as the source of truth unless the design doc is being intentionally updated.

## Security

- Never commit secrets, tokens, private keys, customer data, or other sensitive material.
- Treat request input, database content, storage metadata, and third-party responses as untrusted unless the code documents otherwise.
- Preserve existing auth, validation, and permission boundaries.

## License and Contribution Terms

By contributing to this repository, you confirm that:

- you have the legal right to submit the contribution
- the contribution is your original work, or you otherwise have the right to license it for this project
- you agree that your contribution is provided under the repository's license terms

Maintainers may require additional contributor agreement or sign-off steps for external contributions. If such a process is enabled for pull requests, please complete it before requesting review.

## Style Notes for Commits

If maintainers ask you for a suggested commit message, prefer:

```text
<type>(<scope>): <subject>
```

Examples:

- `fix(auth): reject expired device token refresh`
- `test(repository): cover bundle quota rollback`
- `docs(api): clarify admin error envelope`

## Questions

If you are unsure whether a change belongs in CE or Enterprise, ask before implementing. In general:

- shared self-hosted core behavior belongs here
- commercial billing, licensing, team policy, and Enterprise-only operator behavior belong in the Enterprise repository
