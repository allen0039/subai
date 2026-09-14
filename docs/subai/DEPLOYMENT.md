# Oracle3 deployment status

Updated: 2026-09-15 (Asia/Shanghai).

- Public entry: https://subai.oracle3.mfallen.de (serves the new SubAI/sub2api instance).
- New image: `allen0039/subai-server:sha-b37252640f2053d2793a315614d23cd32f80b709`.
- Published manifest: `sha256:6a8c531623a9a43dae21323bb93814ab31bb58c84ab9e01643e93a16b25d9d07`.
- New deployment directory on `oracle3`: `/opt/1panel/docker/compose/subai-v2`.
- Compose project: `subai-v2`; HTTP address: `127.0.0.1:18080`.
- PostgreSQL 18, Redis 8, and application containers are healthy. Independent volumes are in use.
- Current release: health, login page and public settings returned HTTP 200. Initial administrator credentials no longer authenticate; production authenticated behavior was not retested.
- Admin compliance gate removed by user request. Authenticated admin endpoints returned 200 during the earlier console release; current unauthenticated access returns 401.
- New credentials and preserved original GPT OAuth configuration remain in the server-side `.env` (mode 0600). Credentials are not recorded here.
- Legacy deployment files remain at `/opt/1panel/docker/compose/subai`; the legacy application is stopped with restart policy `no`. Its database remains intact.
- Backup directory on `oracle3`: `/opt/subai-backups/20260914T155423Z` (mode 0700), containing a PostgreSQL custom-format dump and deployment configuration archive.
- Local rollback image on `oracle3`: `subai-rollback:20260914T155423Z`.
- Public proxy configuration: `/opt/1panel/www/sites/subai.oracle3.mfallen.de/proxy/root.conf`.

## Completed cutover

The user chose an empty new database and requested the original port. The new application now binds 127.0.0.1:18080; the existing public proxy configuration was unchanged. Local and public /health and /login returned HTTP 200. The new database contains only the initial administrator and no upstream accounts; no legacy data was imported.

Initial administrator login details are in the ignored local `.planning/subai-admin-login.txt` file (mode 0600); the server-side configuration is authoritative. The administrator can directly access the console and configure upstream accounts.

To roll back, stop the new application service before restarting `subai-server-1`, because both use port 18080. The old database and saved image remain available; do not run the old Compose service with the newly published latest image.

## Console simplification release

Deployed and verified commit `c4822dfab754f552c9eb7486802912c8811267c1` on the existing port 18080, retaining the current new database. Pre-update backup: `/opt/subai-backups/console-20260914T163105Z`. Public health/login return 200, removed social login/model plaza APIs return 404, and an actual browser login confirms no tour/compliance dialog and no removed settings sections, JavaScript errors, or 423 responses.

## Confirm-only email replacement release

Deployed runtime commit `b37252640f2053d2793a315614d23cd32f80b709`; image publication run `34873046171` succeeded. Backup: `/opt/subai-backups/email-20260914T165650Z`. Existing database, credentials and port 18080 retained. Public health/login and disabled-feature checks passed. Initial configured admin credentials returned 401 during the first deployment attempt, causing a rollback; deployment then succeeded using health checks without resetting credentials. Authenticated email-change behavior was verified with an isolated test account, not the production account.
