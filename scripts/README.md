# Conversion verifier

`verify-conversion.sh` is an fnOS/NAS SSH integration verifier for `POST /convert`.
It does not start, stop, restart, exec into, or modify containers, images, volumes,
Docker/Compose files, or application configuration.

## Hard safety prerequisites

Run as a normal, non-root user; root and `sudo` are rejected. Use only a small,
non-sensitive fixture. The source path/content are disclosed to the connector and
Document Server. **All artifacts are retained:** source snapshot and case copies,
converted outputs, response bodies, headers, and curl stderr. Local-tool stderr can
disclose paths or server errors. There is no automatic deletion; repeated runs
accumulate retained data.

The script requires `--confirmed-quota` and `--confirmed-source-static` before it
will start. `--confirmed-quota` is an explicit operator confirmation that the
filesystem holding `--test-dir` **and every writable connector/Document Server work
volume** have hard per-run write quotas or isolated capacity sufficient for this
conversion, temporary files, and retained output. The script cannot limit
application-side conversion output; its `df` check is not a substitute for this hard
prerequisite. `--confirmed-source-static` confirms the source and source parent stay
unchanged throughout snapshotting. Identity/size checks before and after `cp` narrow
accidental changes but cannot eliminate a `cp` open/copy race.

`--test-dir` must be an existing, dedicated absolute directory visible to the
application at exactly the same absolute path. Its immediate entry and source/source
parent entries are checked for current-user ownership and no group/world write bit.
This is not a complete portable trust proof: every ancestor, relevant ACL, mount
configuration, and same-UID process must be trusted and non-writable by an attacker.
The script rejects symlinks at checked entries and rechecks selected device:inode
identities, but does **not** eliminate open/copy TOCTOU, ancestor-symlink, ACL, mount
namespace, root, or compromised-service risks.

Raw source/test-dir inputs and their physically canonicalized forms reject C0
controls (including CR/LF). Physical canonicalization uses a sentinel so command
substitution cannot silently discard a physical path's trailing newline; it is then
rejected. All cases use one controlled run-directory snapshot, never the original
again. The source maximum is **1 MiB**.

The bounded plan retains one snapshot, up to eight normal source copies plus one per
race round, and about ten 1-MiB response bodies plus one per race round. `df -kP`
checks only this **minimum script-artifact** estimate with a 64-MiB margin. It does
not cover conversion output or server temporary files and warns if parsing is not
reliable. `mktemp -d`, a device:inode-capable `stat`, and required curl options are
tested at startup; they are not claimed to be POSIX. Test on the target fnOS
BusyBox/userland and curl build first.

## Endpoint and invocation

`--base-url` accepts only `http://host[:numeric-port]` or
`https://host[:numeric-port]`: nonempty host, port 1–65535, and no credentials,
path, query, fragment, percent encoding, controls, whitespace, backslash, or curl
glob syntax. It is a **trusted connector endpoint**, not an arbitrary URL. Only exact
`localhost` and exact `127.0.0.1` are treated as local for the HTTP warning; every
other HTTP endpoint warns. Use certificate-validated HTTPS for remote endpoints.

```sh
sh scripts/verify-conversion.sh \
  --base-url http://127.0.0.1:9080 \
  --test-dir /vol1/private/convert-verify \
  --source /vol1/private/documents/sample.xls \
  --confirmed-quota \
  --confirmed-source-static
```

Supported inputs: `doc`, `xls`, `ppt`, `odt`, `ods`, `odp`, `rtf`, `txt`, and `csv`.
The external-creator diagnostic is off by default. Explicit opt-in is
`--race-rounds 1` through `3`; it requires `python3 -I -S`.

## Checks, signals, and interpretation

Required checks cover basic conversion, existing marker retention, overwrite,
auto-rename, fallback, exhaustion, invalid flags, and two concurrent requests.
Marker checks reject symlinks and verify unchanged bytes. Fallback checks both
pre-existing candidates; exhaustion checks all eleven pre-existing markers.

The race creator uses `O_CREAT|O_EXCL`. Creator exit `0` means it created, fully
wrote, fsynced, and closed the marker; exit `1` means only that `O_EXCL` lost to an
existing target. Any other creator exit (including marker-read, open, write, fsync,
or close errors) fails the run. If the creator succeeds, only HTTP `409` with a
still-regular, non-symlink, byte-identical marker is an *observed compatible* warning.
HTTP `200`, marker change/disappearance/symlink replacement, or request errors fail.
If the creator did not acquire the marker, that is only a warning, never atomicity
proof. This cannot determine the final `renameat2` no-replace publication window.

Each curl is directly started by the main shell; normal serial/concurrent flows wait
for their own child status. To avoid PID-reuse risk, signal and EXIT traps never
signal or wait on cached PIDs. HUP, INT, QUIT, and TERM only prevent trap recursion,
retain artifacts, and exit `128+signal`. A started curl may continue until its
configured `--max-time` (180 seconds for conversion; 30 seconds for preflight); the
remote service may continue longer and is not guaranteed to cancel conversion.
Curl starts with `--disable`, disables globbing/proxies, restricts protocols, uses
timeouts, unsets common TLS/logging environment overrides, and limits response body
size. Headers are retained but are not covered by `--max-filesize`.

Exit status: `0` required checks passed (warnings allowed); `1` assertion failure;
`2` unsafe/invalid invocation; `3` missing capability or service/environment failure.

Run deterministic tests as well:

```sh
go test ./internal/server ./internal/file
```
