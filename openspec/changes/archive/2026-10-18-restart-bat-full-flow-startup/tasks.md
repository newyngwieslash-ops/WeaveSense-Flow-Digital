# Tasks: restart.bat full startup for WeaveSense-Flow

- [x] 1. Write root `restart.bat`: env bootstrap + port guard + compose up + readiness wait + browser open; saved with CRLF
- [x] 2. Verify: `docker compose -f docker-compose.flow.yml --env-file .env.flow config` parses and contains `published: 8088`; dry-path checks (port-busy branch, existing `.env.flow` untouched); commit
