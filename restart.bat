@echo off
rem ===========================================================================
rem  WeaveSense-Flow - one-click (re)start of the full Flow stack
rem
rem  Behaviour
rem    .env.flow missing -> copied from .env.flow.example with random secrets
rem    starts docker-compose.flow.yml (postgres/redis/minio/platform/web/
rem    loom-engine/loom-scheduler) on the single published port 8088
rem    readiness = GET /api/health returning "ready":true, then browser opens
rem    restart.bat smoke   -> also run deploy/flow/smoke.sh (Git Bash) when ready
rem
rem  Port 8088 is read-only guarded: if a foreign process holds it the script
rem  reports the owner and starts nothing. It never force-kills processes.
rem  (services/loom-engine/restart.bat covers the legacy sub-project only.)
rem
rem  Usage docs: docs/flow/local-acceptance.md ; smoke: deploy/flow/smoke.sh
rem ===========================================================================
setlocal EnableExtensions
cd /d "%~dp0"

set "PORT=8088"
set "COMPOSE_FILE=docker-compose.flow.yml"
set "ENV_FILE=.env.flow"
set "LOG_DIR=.local\logs"
set "LOG_FILE=%LOG_DIR%\restart.log"
set "RUN_SMOKE=0"
if /i "%~1"=="smoke" set "RUN_SMOKE=1"

if not exist "%LOG_DIR%" mkdir "%LOG_DIR%" >nul 2>&1

echo ==========================================
echo   WeaveSense-Flow  full stack  restart
echo ==========================================
echo.

call :log "--- restart.bat (flow) run --- cwd=%CD%"

rem ---------- [1/5] environment file ----------
rem A fresh copy gets random secrets; an existing file that still contains any
rem `change-me` placeholder is rejected: the platform silently runs with a
rem too-short JWT signing key (Enabled() requires >= 32 bytes) otherwise.
if exist "%ENV_FILE%" goto check_env_placeholders
if not exist ".env.flow.example" goto err_noenvexample
goto do_bootstrap_env

:check_env_placeholders
findstr /c:"change-me" "%ENV_FILE%" >nul 2>&1
if errorlevel 1 goto env_ready
goto env_has_placeholder

:do_bootstrap_env
echo [1/5] Creating %ENV_FILE% from .env.flow.example with random secrets...
copy /y ".env.flow.example" "%ENV_FILE%" >nul || goto err_noenvexample
call :gen_secret SECRET1
call :gen_secret SECRET2
call :gen_secret SECRET3
call :gen_secret SECRET4
call :gen_secret SECRET5
call :gen_secret SECRET6
if not defined SECRET1 goto err_nosecretgen
call :replace_in_env "POSTGRES_SUPERUSER_PASSWORD=change-me" "POSTGRES_SUPERUSER_PASSWORD=%SECRET1%" || goto err_envwrite
call :replace_in_env "PLATFORM_DB_PASSWORD=change-me" "PLATFORM_DB_PASSWORD=%SECRET2%" || goto err_envwrite
call :replace_in_env "INTEL_DB_PASSWORD=change-me" "INTEL_DB_PASSWORD=%SECRET3%" || goto err_envwrite
call :replace_in_env "REDIS_PASSWORD=change-me" "REDIS_PASSWORD=%SECRET4%" || goto err_envwrite
call :replace_in_env "MINIO_ROOT_PASSWORD=change-me-min-8-chars" "MINIO_ROOT_PASSWORD=%SECRET5%" || goto err_envwrite
call :replace_in_env "FLOW_INTERNAL_JWT_SIGNING_KEY=change-me-32-bytes-min" "FLOW_INTERNAL_JWT_SIGNING_KEY=%SECRET6%%SECRET1%%SECRET2%" || goto err_envwrite
rem Post-bootstrap guard: a drifted needle or silent no-op must never leave
rem placeholder secrets behind (the platform would boot with a short JWT key).
findstr /c:"change-me" "%ENV_FILE%" >nul 2>&1 && goto env_has_placeholder
call :log "step 1: %ENV_FILE% bootstrapped with random secrets"
goto env_ready

:env_has_placeholder
echo [ERROR] %ENV_FILE% still contains change-me placeholders.
echo         Fix them (random values, FLOW_INTERNAL_JWT_SIGNING_KEY needs 32+ bytes)
echo         or delete %ENV_FILE% so this script recreates it with random secrets.
call :log "FATAL %ENV_FILE% still has change-me placeholders"
goto fail

:env_ready
rem Read FLOW_HTTP_PORT out of the env file so the wait loop uses the real port.
for /f "usebackq tokens=1,* delims==" %%A in ("%ENV_FILE%") do (
  if /i "%%A"=="FLOW_HTTP_PORT" set "PORT=%%B"
)
rem Strip possible surrounding whitespace from the value.
for /f "tokens=* delims= " %%V in ("%PORT%") do set "PORT=%%V"

where docker >nul 2>&1
if errorlevel 1 goto err_nodocker
docker info >nul 2>&1
if errorlevel 1 goto err_nodaemon

rem Compose must still publish the fixed port (design guard: no silent move).
set "CFG_OUT=%TEMP%\flow-compose-config.txt"
del "%CFG_OUT%" >nul 2>&1
call :compose config >"%CFG_OUT%" 2>nul
set "PORT_MAPPED="
for /f "tokens=2 delims=: " %%P in ('findstr /c:"published:" "%CFG_OUT%" 2^>nul') do (
  if "%%~P"=="%PORT%" set "PORT_MAPPED=1"
)
del "%CFG_OUT%" >nul 2>&1
if not defined PORT_MAPPED goto err_portconfig

rem curl is the readiness probe; missing it would just time out confusingly.
where curl.exe >nul 2>&1
if errorlevel 1 goto err_nocurl

rem ---------- [2/5] port guard ----------
echo [2/5] Checking port %PORT% ...
call :detect_port
if "%APP_WAS_RUNNING%"=="1" goto port_ours
if defined PORT_OWNER_PID goto err_portbusy
echo       Port %PORT% is free.
call :log "step 2: port %PORT% free"
goto port_checked

:port_ours
echo       Port %PORT% is held by this project (web container). Restarting it.
call :log "step 2: port %PORT% owned by this compose project - down then up"
call :compose down
if errorlevel 1 echo [WARN] compose down failed, continue anyway.

:port_checked
rem ---------- [3/5] build and start ----------
echo [3/5] Build and start the Flow stack (this can take minutes on first run)...
call :log "step 3: compose up --build -d"
call :compose up --build -d
if not errorlevel 1 goto stack_started
echo [WARN] Build failed. Retrying with existing local images...
call :log "step 3: retry compose up --no-build -d"
call :compose up --no-build -d
if errorlevel 1 goto err_up

:stack_started

rem ---------- [4/5] readiness ----------
echo [4/5] Waiting for http://127.0.0.1:%PORT%/api/health ...
call :log "step 4: wait for readiness"
set /a WAITED=0
set /a MAXWAIT=600

:wait_loop
curl.exe -sf --max-time 3 "http://127.0.0.1:%PORT%/api/health" 2>nul | findstr /c:"\"ready\":true" >nul 2>&1
if not errorlevel 1 goto ready
curl.exe -sf --max-time 3 "http://127.0.0.1:%PORT%/" >nul 2>&1
if not errorlevel 1 goto web_up_no_health
call :sleep 3
set /a WAITED+=3
if %WAITED% LSS %MAXWAIT% goto wait_loop
goto err_timeout

:web_up_no_health
echo [WARN] Web answers on %PORT% but /api/health is not ready yet; waiting 60s more for the platform...
set /a WAITED=0
:wait_health_loop
curl.exe -sf --max-time 3 "http://127.0.0.1:%PORT%/api/health" 2>nul | findstr /c:"\"ready\":true" >nul 2>&1
if not errorlevel 1 goto ready
call :sleep 3
set /a WAITED+=3
if %WAITED% LSS 60 goto wait_health_loop
echo [WARN] Proceeding WITHOUT a ready platform (web serves, health never turned ready).
echo         Check: docker compose -f %COMPOSE_FILE% --env-file %ENV_FILE% logs platform
call :log "step 4: DEGRADED - web up but health not ready after grace period; proceeding"
goto degraded_open

:ready
echo [5/5] Ready after %WAITED%s. Opening browser...
call :log "step 5: ready after %WAITED%s"
start "" "http://127.0.0.1:%PORT%/"
goto post_open

:degraded_open
start "" "http://127.0.0.1:%PORT%/"

:post_open
echo.
echo   Stack:    weavesense-flow  (compose project)
echo   URL:      http://127.0.0.1:%PORT%/
echo   Log:      %LOG_FILE%
echo   Profiles: edit %ENV_FILE% COMPOSE_PROFILES to add telegram/wechat/extract/research/backup/monitoring
echo.
if "%RUN_SMOKE%"=="1" goto run_smoke
echo Done. Press any key to close.
pause >nul
exit /b 0

:run_smoke
echo Running end-to-end smoke test (deploy/flow/smoke.sh)...
where bash >nul 2>&1
if errorlevel 1 goto err_nobash
bash deploy/flow/smoke.sh "http://127.0.0.1:%PORT%"
echo.
echo Smoke finished. Press any key to close.
pause >nul
exit /b %errorlevel%

rem --------------------------------- helpers ---------------------------------
:detect_port
set "APP_WAS_RUNNING=0"
set "PORT_OWNER_PID="
set "PORT_OWNER_NAME="
set "OTHER_CONTAINER="
for /f "usebackq delims=" %%P in (`powershell -NoProfile -Command "(Get-NetTCPConnection -LocalPort %PORT% -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty OwningProcess)" 2^>nul`) do set "PORT_OWNER_PID=%%P"
rem This project owns the port iff its own web container is currently published on it.
for /f "usebackq delims=" %%C in (`docker ps --filter "publish=%PORT%" --filter "label=com.docker.compose.project=weavesense-flow" -q 2^>nul`) do set "APP_WAS_RUNNING=1"
if not defined PORT_OWNER_PID exit /b 0
if "%APP_WAS_RUNNING%"=="1" exit /b 0
for /f "usebackq delims=" %%N in (`docker ps --filter "publish=%PORT%" --format "{{.Names}}" 2^>nul`) do set "OTHER_CONTAINER=%%N"
if defined OTHER_CONTAINER exit /b 0
for /f "tokens=1 delims=," %%N in ('tasklist /fi "PID eq %PORT_OWNER_PID%" /fo csv /nh 2^>nul') do if /i not "%%~N"=="INFO:" if not defined PORT_OWNER_NAME set "PORT_OWNER_NAME=%%~N"
exit /b 0

:gen_secret
rem %1 = output var name. openssl first (Git for Windows), PowerShell GUID fallback.
set "%~1="
for /f "usebackq delims=" %%G in (`openssl rand -hex 16 2^>nul`) do set "%~1=%%G"
if defined %~1 exit /b 0
for /f "usebackq delims=" %%G in (`powershell -NoProfile -Command "[guid]::NewGuid().ToString('N')+[guid]::NewGuid().ToString('N')"`) do set "%~1=%%G"
exit /b 0

:replace_in_env
rem %1 = <needle>  %2 = <replacement>. One PowerShell call per line; file is small.
powershell -NoProfile -Command "$f='%ENV_FILE%';$t=Get-Content $f -Raw;$t=$t.Replace('%~1','%~2');Set-Content -Path $f -Value $t -NoNewline -Encoding UTF8" >nul 2>&1
if errorlevel 1 exit /b 1
exit /b 0

:compose
docker compose -f "%COMPOSE_FILE%" --env-file "%ENV_FILE%" %*
exit /b %errorlevel%

:sleep
powershell -NoProfile -Command "Start-Sleep -Seconds %~1" >nul 2>&1
exit /b 0

:log
echo %~1
>>"%LOG_FILE%" echo [%DATE% %TIME%] %~1
exit /b 0

rem ---------------------------------- errors ---------------------------------
:err_noenvexample
echo [ERROR] %ENV_FILE% missing and .env.flow.example not found.
echo         Run from the repository root (next to docker-compose.flow.yml).
call :log "FATAL missing %ENV_FILE% and .env.flow.example"
goto fail

:err_nosecretgen
echo [ERROR] Could not generate random secrets (openssl and PowerShell both failed).
echo         Create %ENV_FILE% manually from .env.flow.example.
call :log "FATAL secret generation failed"
goto fail

:err_envwrite
echo [ERROR] Failed writing secrets into %ENV_FILE%.
call :log "FATAL env write failed"
goto fail

:err_nodocker
echo [ERROR] Docker not found in PATH. Install Docker Desktop, start it, then retry.
call :log "FATAL docker not found"
goto fail

:err_nodaemon
echo [ERROR] Docker engine is not running. Start Docker Desktop, then retry.
echo         Volumes and data were not touched.
call :log "FATAL docker engine not ready"
goto fail

:err_nocurl
echo [ERROR] curl.exe not found in PATH (built into Windows since 1803).
echo         Install curl or update Windows; the readiness probe needs it.
call :log "FATAL curl.exe missing"
goto fail

:err_portconfig
echo [ERROR] %COMPOSE_FILE% no longer publishes port %PORT%.
echo         This launcher starts the app on the fixed port %PORT%; refusing to
echo         start it somewhere else. Update FLOW_HTTP_PORT here and the compose
echo         ports mapping together if %PORT% is really being changed.
call :log "FATAL compose no longer publishes %PORT%"
goto fail

:err_portbusy
echo.
echo [ERROR] Port %PORT% is in use by something that is NOT this project.
echo.
if defined OTHER_CONTAINER echo         Docker container : %OTHER_CONTAINER%
if defined PORT_OWNER_NAME echo         Process          : %PORT_OWNER_NAME%  (PID %PORT_OWNER_PID%)
if not defined PORT_OWNER_NAME if not defined OTHER_CONTAINER echo         PID              : %PORT_OWNER_PID%
echo         Local address    : 127.0.0.1:%PORT%
echo.
echo         Nothing was stopped and the stack was NOT started. Free the port or
echo         stop the other app, then run this script again.
call :log "FATAL port %PORT% busy: container=%OTHER_CONTAINER% process=%PORT_OWNER_NAME% pid=%PORT_OWNER_PID%"
goto fail

:err_up
echo [ERROR] docker compose up failed. Scroll up for the log.
call :log "FATAL compose up failed"
goto fail

:err_timeout
echo [ERROR] Stack not ready after about 10 minutes.
echo         Containers were left running. Check: docker compose -f %COMPOSE_FILE% --env-file %ENV_FILE% logs
call :log "FATAL not ready after %MAXWAIT%s"
goto fail

:err_nobash
echo [WARN] bash not found; cannot run deploy/flow/smoke.sh. Install Git for Windows (in PATH) and retry.
call :log "smoke skipped: bash not found"
echo.
echo Done. Press any key to close.
pause >nul
exit /b 1

:fail
echo.
echo Log: %LOG_FILE%
echo Press any key to close.
pause >nul
exit /b 1
