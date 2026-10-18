# flow-startup Delta

## ADDED Requirements

### Requirement: One-click full stack start

The repository root MUST provide a `restart.bat` that starts the complete WeaveSense-Flow stack defined in `docker-compose.flow.yml` (postgres, redis, minio, platform, web, loom-engine, loom-scheduler).

#### Scenario: First run on a clean machine

- **WHEN** `.env.flow` does not exist
- **THEN** the script copies `.env.flow.example` to `.env.flow`, replaces every `change-me` secret placeholder with a generated random value, and proceeds to start the stack
- **AND** if any placeholder survives the replacement (drifted needles or a failed write), the script aborts instead of starting with placeholder credentials
- **AND** the stack is reachable at `http://127.0.0.1:8088` with `/api/health` returning `"ready":true`

#### Scenario: Port 8088 held by a foreign process

- **WHEN** port 8088 is in use by something that is not this compose project
- **THEN** the script reports the owner and starts nothing, without killing any process

#### Scenario: Compose no longer publishes the fixed port

- **WHEN** the resolved `docker compose config` does not contain `published: <FLOW_HTTP_PORT>`
- **THEN** the script aborts with a clear error instead of silently starting on another port

#### Scenario: Existing environment is preserved

- **WHEN** `.env.flow` already exists
- **THEN** the script starts the stack using it unchanged
- **AND** a file that still contains any `change-me` placeholder is rejected with remediation instructions (a short `FLOW_INTERNAL_JWT_SIGNING_KEY` would silently disable the engine proxy)
