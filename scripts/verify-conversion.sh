#!/bin/sh
# fnOS/NAS verifier for POST /convert.  It never manages containers.

PATH=/usr/sbin:/usr/bin:/sbin:/bin
export PATH
LC_ALL=C
export LC_ALL
unset CURL_HOME SSLKEYLOGFILE CURL_CA_BUNDLE SSL_CERT_FILE SSL_CERT_DIR
set -u
set -f
umask 077

BASE_URL='http://127.0.0.1:9080'
TEST_DIR='' SOURCE='' RACE_ROUNDS=0 VERBOSE=0 CONFIRMED_QUOTA=0 CONFIRMED_SOURCE_STATIC=0
MAX_SOURCE_BYTES=1048576
MAX_RESPONSE_BYTES=1048576
SPACE_MARGIN_BYTES=67108864
FAILURES=0 WARNINGS=0 RUN_DIR='' RUN_ID='' TEST_ID='' SOURCE_PARENT_ID=''
SOURCE_ID='' SOURCE_SIZE='' SNAPSHOT='' TRAP_ACTIVE=0 PHYSICAL_PATH=''
RECORD_LF='
'

usage() {
    printf '%s\n' \
      'Usage: scripts/verify-conversion.sh --test-dir ABSOLUTE_DIR --source ABSOLUTE_FILE [options]' \
      '' \
      'Options:' \
      '  --base-url URL       trusted connector origin (default: http://127.0.0.1:9080)' \
      '  --test-dir DIR       dedicated, trusted absolute test parent (required)' \
      '  --source FILE        readable absolute legacy document to snapshot (required)' \
      '  --confirmed-quota    confirm hard quotas/isolated capacity for every writable service volume' \
      '  --confirmed-source-static  confirm source and parent stay unchanged while snapshot is made' \
      '  --race-rounds N      opt-in O_EXCL race attempts, 0..3 (default: 0)' \
      '  --verbose            show case labels and HTTP statuses only' \
      '  --help               show this help'
}
note() { printf '%s\n' "$*"; }
pass() { note "PASS: $*"; }
warn() { WARNINGS=$((WARNINGS + 1)); note "WARN: $*"; }
fail() { FAILURES=$((FAILURES + 1)); note "FAIL: $*"; }
die_param() { note "ERROR: $*" >&2; exit 2; }
die_env() { note "ERROR: $*" >&2; exit 3; }
is_uint() { case $1 in ''|*[!0-9]*) return 1 ;; *) return 0 ;; esac; }
is_absolute() { case $1 in /*) return 0 ;; *) return 1 ;; esac; }
has_c0() {
    case $1 in *"$(printf '\r')"*|*'
'*) return 0 ;; esac
    nonprint=$(printf '%s' "$1" | tr -d '[:print:]')
    [ -n "$nonprint" ]
}
physical_dir() {
    # A successful pwd record is PATH + one record LF + sentinel.  The sentinel
    # prevents command substitution from trimming either LF.  Remove exactly the
    # record LF: an LF belonging to PATH remains and is rejected by has_c0.
    physical_capture=$(CDPATH= cd -- "$1" && pwd -P && printf '__FNOS_VERIFY_PWD_SENTINEL__') || return 1
    case $physical_capture in
        *__FNOS_VERIFY_PWD_SENTINEL__)
            PHYSICAL_PATH=${physical_capture%__FNOS_VERIFY_PWD_SENTINEL__}
            case $PHYSICAL_PATH in
                *"$RECORD_LF") PHYSICAL_PATH=${PHYSICAL_PATH%"$RECORD_LF"} ;;
                *) return 1 ;;
            esac
            ;;
        *) return 1 ;;
    esac
}

while [ "$#" -gt 0 ]; do
    case $1 in
        --base-url) [ "$#" -ge 2 ] || die_param '--base-url requires a value'; BASE_URL=$2; shift 2 ;;
        --test-dir) [ "$#" -ge 2 ] || die_param '--test-dir requires a value'; TEST_DIR=$2; shift 2 ;;
        --source) [ "$#" -ge 2 ] || die_param '--source requires a value'; SOURCE=$2; shift 2 ;;
        --confirmed-quota) CONFIRMED_QUOTA=1; shift ;;
        --confirmed-source-static) CONFIRMED_SOURCE_STATIC=1; shift ;;
        --race-rounds) [ "$#" -ge 2 ] || die_param '--race-rounds requires a value'; RACE_ROUNDS=$2; shift 2 ;;
        --verbose) VERBOSE=1; shift ;;
        --help) usage; exit 0 ;;
        *) die_param "unknown option: $1" ;;
    esac
done

[ "$(id -u)" != 0 ] || die_param 'refusing to run as root (do not use sudo)'
[ -n "$TEST_DIR" ] && [ -n "$SOURCE" ] || die_param '--test-dir and --source are required'
[ "$CONFIRMED_QUOTA" -eq 1 ] || die_param '--confirmed-quota is required'
[ "$CONFIRMED_SOURCE_STATIC" -eq 1 ] || die_param '--confirmed-source-static is required'
has_c0 "$TEST_DIR" && die_param '--test-dir contains a C0 control character'
has_c0 "$SOURCE" && die_param '--source contains a C0 control character'
is_absolute "$TEST_DIR" && is_absolute "$SOURCE" || die_param '--test-dir and --source must be absolute'
is_uint "$RACE_ROUNDS" && [ "$RACE_ROUNDS" -le 3 ] || die_param '--race-rounds must be an integer from 0 to 3'
has_c0 "$BASE_URL" && die_param 'unsafe --base-url control character'
case $BASE_URL in *'%'*|*'@'*|*'/'*'/'*'/'*|*'?'*|*'#'*|*' '*|*'\'*|*'['*|*']'*|*'{'*|*'}'*|*'&'*|*'='*|*'*'*|*'$'* ) die_param 'unsafe --base-url' ;; esac
case $BASE_URL in http://*|https://*) ;; *) die_param '--base-url must be http://host[:numeric-port] or https://host[:numeric-port]' ;; esac
BASE_AUTHORITY=${BASE_URL#http://}; [ "$BASE_AUTHORITY" = "$BASE_URL" ] && BASE_AUTHORITY=${BASE_URL#https://}
[ -n "$BASE_AUTHORITY" ] || die_param '--base-url host must not be empty'
case $BASE_AUTHORITY in *[!abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.:-]*) die_param 'unsafe --base-url host' ;; esac
case $BASE_AUTHORITY in
    *:*) BASE_HOST=${BASE_AUTHORITY%:*}; BASE_PORT=${BASE_AUTHORITY##*:}; [ "$BASE_HOST" != "$BASE_AUTHORITY" ] && [ -n "$BASE_HOST" ] && is_uint "$BASE_PORT" && [ "${#BASE_PORT}" -le 5 ] && [ "$BASE_PORT" -ge 1 ] && [ "$BASE_PORT" -le 65535 ] || die_param '--base-url port must be numeric 1..65535' ;;
    *) BASE_HOST=$BASE_AUTHORITY ;;
esac
case $BASE_HOST in ''|*:*|.*|*..*|*.) die_param 'unsafe --base-url host' ;; esac
case $BASE_URL in http://*) case $BASE_HOST in localhost|127.0.0.1) ;; *) warn 'HTTP endpoint is not exact localhost or 127.0.0.1; use certificate-validated HTTPS' ;; esac ;; esac

for cmd in curl mktemp cp mkdir cmp id ls stat df wc tr pwd dirname basename sed; do command -v "$cmd" >/dev/null 2>&1 || die_env "required command unavailable: $cmd"; done
# Parse every curl option at startup; mktemp/stat option variants are tested below.
curl --disable --globoff --proto '=http,https' --noproxy '*' --connect-timeout 1 --max-time 1 --max-filesize "$MAX_RESPONSE_BYTES" --silent --show-error --version >/dev/null 2>&1 || die_env 'curl lacks a required safety option'
[ ! -L "$SOURCE" ] && [ -f "$SOURCE" ] && [ -r "$SOURCE" ] || die_param '--source must be a readable non-symlink regular file'
[ ! -L "$TEST_DIR" ] && [ -d "$TEST_DIR" ] && [ -w "$TEST_DIR" ] && [ -x "$TEST_DIR" ] || die_param '--test-dir must be a writable non-symlink directory'
physical_dir "$TEST_DIR" || die_param 'cannot physically normalize --test-dir'
TEST_DIR=$PHYSICAL_PATH
has_c0 "$TEST_DIR" && die_param 'physical --test-dir contains a C0 control character'
SOURCE_PARENT_INPUT=$(dirname -- "$SOURCE") || die_param 'cannot determine source parent'
has_c0 "$SOURCE_PARENT_INPUT" && die_param 'source parent contains a C0 control character'
physical_dir "$SOURCE_PARENT_INPUT" || die_param 'cannot physically normalize source parent'
SOURCE_PARENT=$PHYSICAL_PATH
has_c0 "$SOURCE_PARENT" && die_param 'physical source parent contains a C0 control character'
SOURCE=$SOURCE_PARENT/$(basename -- "$SOURCE")
has_c0 "$SOURCE" && die_param 'physical source contains a C0 control character'

owner_mode_ok() {
    entry=$(ls -dn -- "$1") || return 1
    set -- $entry
    [ "$3" = "$(id -u)" ] || return 1
    case $1 in ?????w*|????????w*) return 1 ;; esac
}
identity() {
    value=$(stat -c '%d:%i' -- "$1" 2>/dev/null) || value=$(stat -f '%d:%i' "$1" 2>/dev/null) || return 1
    case $value in *[!0-9:]*|*:*:*) return 1 ;; [0-9]*:[0-9]*) printf '%s\n' "$value" ;; *) return 1 ;; esac
}
source_valid() { [ ! -L "$SOURCE" ] && [ -f "$SOURCE" ] && [ -r "$SOURCE" ]; }
source_stable() {
    source_valid || return 1
    current_size=$(wc -c <"$SOURCE" | tr -d '[:space:]') || return 1
    source_valid && is_uint "$current_size" && [ "$current_size" -le "$MAX_SOURCE_BYTES" ] && [ "$current_size" = "$SOURCE_SIZE" ] && [ "$(identity "$SOURCE_PARENT")" = "$SOURCE_PARENT_ID" ] && [ "$(identity "$SOURCE")" = "$SOURCE_ID" ]
}

case $TEST_DIR in /|/home|/root|/tmp|/var|/usr|/etc|/opt|/mnt|/media|/vol[0-9]|/vol[0-9][0-9]|/vol[0-9][0-9][0-9]) die_param 'unsafe test parent rejected' ;; esac
owner_mode_ok "$TEST_DIR" || die_param '--test-dir must be owned by this user and not group/world writable'
owner_mode_ok "$SOURCE_PARENT" || die_param 'source parent must be owned by this user and not group/world writable'
owner_mode_ok "$SOURCE" || die_param '--source must be owned by this user and not group/world writable'
TEST_ID=$(identity "$TEST_DIR") || die_env 'this stat variant cannot report device:inode for --test-dir'
SOURCE_PARENT_ID=$(identity "$SOURCE_PARENT") || die_env 'this stat variant cannot report device:inode for source parent'
SOURCE_ID=$(identity "$SOURCE") || die_env 'this stat variant cannot report device:inode for source'
SOURCE_SIZE=$(wc -c <"$SOURCE" | tr -d '[:space:]') || die_env 'cannot measure source size'
is_uint "$SOURCE_SIZE" && [ "$SOURCE_SIZE" -le "$MAX_SOURCE_BYTES" ] || die_param 'source exceeds the 1 MiB maximum'

SOURCE_NAME=$(basename -- "$SOURCE"); SOURCE_EXT=${SOURCE_NAME##*.}
[ "$SOURCE_EXT" != "$SOURCE_NAME" ] || die_param 'source needs a supported extension'
SOURCE_EXT=$(printf '%s' "$SOURCE_EXT" | tr '[:upper:]' '[:lower:]')
case $SOURCE_EXT in doc|odt|rtf|txt) TARGET_EXT=docx ;; xls|ods|csv) TARGET_EXT=xlsx ;; ppt|odp) TARGET_EXT=pptx ;; *) die_param 'unsupported source extension' ;; esac

# SOURCE_SIZE <= 1 MiB and (9 + rounds) <= 12, so this stays below signed 32-bit arithmetic.
COPY_COUNT=$((9 + RACE_ROUNDS))
NEEDED_BYTES=$((SOURCE_SIZE * COPY_COUNT + (10 + RACE_ROUNDS) * MAX_RESPONSE_BYTES + SPACE_MARGIN_BYTES))
df_line=$(df -kP "$TEST_DIR" 2>/dev/null | sed -n '2p') || df_line=''
if [ -n "$df_line" ]; then
    set -- $df_line; available_kb=${4-}
    if is_uint "$available_kb"; then
        needed_kb=$(( (NEEDED_BYTES + 1023) / 1024 ))
        [ "$available_kb" -ge "$needed_kb" ] || die_env 'insufficient space for the minimum retained script artifacts'
    else warn 'could not reliably parse minimum script-artifact space; this does not cover conversion or service output'; fi
else warn 'df space preflight unavailable; this does not cover conversion or service output'; fi

RUN_DIR=$(mktemp -d "$TEST_DIR/fnos-convert-verify.XXXXXXXX") || die_env 'mktemp -d failed to create run directory'
case $RUN_DIR in "$TEST_DIR"/fnos-convert-verify.*) ;; *) die_env 'mktemp returned a path outside test parent' ;; esac
[ ! -L "$RUN_DIR" ] && [ -d "$RUN_DIR" ] || die_env 'run directory safety validation failed'
RUN_ID=$(identity "$RUN_DIR") || die_env 'this stat variant cannot report device:inode for run directory'
[ "$(identity "$TEST_DIR")" = "$TEST_ID" ] || die_env 'test parent changed during setup'

wait_curl() { wait "$1"; }
on_exit() { trap - 0 HUP INT QUIT TERM; }
on_signal() {
    sig=$1
    [ "$TRAP_ACTIVE" -eq 0 ] || exit $((128 + sig))
    TRAP_ACTIVE=1
    trap - 0 HUP INT QUIT TERM
    note 'ERROR: signal received; retaining artifacts; curl requests rely on their configured max-time' >&2
    exit $((128 + sig))
}
trap 'on_signal 1' HUP
trap 'on_signal 2' INT
trap 'on_signal 3' QUIT
trap 'on_signal 15' TERM
trap 'on_exit' 0

assert_run_identity() { [ ! -L "$RUN_DIR" ] && [ -d "$RUN_DIR" ] && [ "$(identity "$TEST_DIR")" = "$TEST_ID" ] && [ "$(identity "$RUN_DIR")" = "$RUN_ID" ]; }
RESP_DIR=$RUN_DIR/responses
mkdir -- "$RESP_DIR" || die_env 'cannot create controlled response directory'
assert_run_identity && source_stable || die_env 'run directory, source, or source parent changed before snapshot'
SNAPSHOT=$RUN_DIR/source-snapshot.$SOURCE_EXT
cp -- "$SOURCE" "$SNAPSHOT" || die_env 'cannot create controlled source snapshot'
assert_run_identity && source_stable || die_env 'run directory, source, or source parent changed during snapshot'
[ ! -L "$SNAPSHOT" ] && [ -f "$SNAPSHOT" ] && [ "$(wc -c <"$SNAPSHOT")" = "$SOURCE_SIZE" ] || die_env 'controlled source snapshot validation failed'

read_code() { code=000; [ -r "$1" ] && { IFS= read -r code <"$1" || :; }; printf '%s\n' "$code"; }
nonempty_regular() { [ -f "$1" ] && [ ! -L "$1" ] && [ -s "$1" ]; }
marker() { printf 'fnos-convert-verify-marker:%s\n' "$1" >"$2" || return 1; [ -f "$2" ] && [ ! -L "$2" ] && [ -s "$2" ]; }
marker_unchanged() { [ -f "$2" ] && [ ! -L "$2" ] && cmp -s "$1" "$2"; }
copy_case() {
    assert_run_identity || return 1
    CASE_DIR=$RUN_DIR/$1; mkdir -- "$CASE_DIR" || return 1
    CASE_SOURCE=$CASE_DIR/input.$SOURCE_EXT; cp -- "$SNAPSHOT" "$CASE_SOURCE" || return 1
    [ -f "$CASE_SOURCE" ] && [ ! -L "$CASE_SOURCE" ] && [ "$(wc -c <"$CASE_SOURCE")" = "$SOURCE_SIZE" ] || return 1
    CASE_TARGET=$CASE_DIR/input.$TARGET_EXT
}
start_post() {
    label=$1 path=$2 query=$3
    assert_run_identity || return 1
    body=$RESP_DIR/$label.body; headers=$RESP_DIR/$label.headers; err=$RESP_DIR/$label.curl.stderr; code_file=$RESP_DIR/$label.code
    url=$BASE_URL/convert; [ -z "$query" ] || url=$url?$query
    curl --disable --globoff --proto '=http,https' --noproxy '*' --connect-timeout 10 --max-time 180 --max-filesize "$MAX_RESPONSE_BYTES" --silent --show-error --output "$body" --dump-header "$headers" --write-out '%{http_code}' --request POST --data-urlencode "path=$path" "$url" >"$code_file" 2>"$err" &
    CURL_PID=$!
}
start_preflight() {
    assert_run_identity || return 1
    curl --disable --globoff --proto '=http,https' --noproxy '*' --connect-timeout 10 --max-time 30 --max-filesize "$MAX_RESPONSE_BYTES" --silent --show-error --output "$RESP_DIR/preflight.body" --dump-header "$RESP_DIR/preflight.headers" --write-out '%{http_code}' --request POST "$BASE_URL/convert" >"$RESP_DIR/preflight.code" 2>"$RESP_DIR/preflight.curl.stderr" &
    CURL_PID=$!
}
post_convert() { start_post "$1" "$2" "$3" || return 1; pid=$CURL_PID; wait_curl "$pid" || return 1; POST_CODE=$(read_code "$RESP_DIR/$1.code"); [ "$POST_CODE" != 000 ] || return 1; [ "$VERBOSE" -eq 0 ] || note "INFO: $1 HTTP $POST_CODE"; }

start_preflight || die_env 'preflight setup failed'; preflight_pid=$CURL_PID; wait_curl "$preflight_pid" || die_env 'POST /convert is unreachable (details retained in artifacts)'; [ "$(read_code "$RESP_DIR/preflight.code")" != 000 ] || die_env 'POST /convert is unreachable (details retained in artifacts)'
pass 'preflight POST /convert reachable'
copy_case basic || die_env 'basic case setup failed'; post_convert basic "$CASE_SOURCE" '' || die_env 'basic conversion curl/service failure'; [ "$POST_CODE" = 200 ] && nonempty_regular "$CASE_TARGET" && pass 'basic conversion' || fail 'basic conversion did not return 200 with a non-empty target'
copy_case existing || die_env 'existing case setup failed'; MARKER=$CASE_DIR/expected.marker; marker existing "$MARKER" && cp -- "$MARKER" "$CASE_TARGET" || die_env 'existing marker setup failed'; post_convert existing "$CASE_SOURCE" '' || die_env 'existing case curl/service failure'; [ "$POST_CODE" = 409 ] && marker_unchanged "$MARKER" "$CASE_TARGET" && pass 'existing target returns 409 and preserves marker' || fail 'existing target assertion failed'
copy_case overwrite || die_env 'overwrite case setup failed'; MARKER=$CASE_DIR/expected.marker; marker overwrite "$MARKER" && cp -- "$MARKER" "$CASE_TARGET" || die_env 'overwrite marker setup failed'; post_convert overwrite "$CASE_SOURCE" 'overwrite=true' || die_env 'overwrite curl/service failure'; [ "$POST_CODE" = 200 ] && nonempty_regular "$CASE_TARGET" && ! cmp -s "$MARKER" "$CASE_TARGET" && pass 'overwrite=true replaces target' || fail 'overwrite assertion failed'
copy_case rename || die_env 'rename case setup failed'; MARKER=$CASE_DIR/expected.marker; marker rename "$MARKER" && cp -- "$MARKER" "$CASE_TARGET" || die_env 'rename marker setup failed'; post_convert rename "$CASE_SOURCE" 'auto_rename=true' || die_env 'rename curl/service failure'; [ "$POST_CODE" = 200 ] && marker_unchanged "$MARKER" "$CASE_TARGET" && nonempty_regular "$CASE_DIR/input (converted).$TARGET_EXT" && pass 'auto-rename first candidate' || fail 'auto-rename assertion failed'
copy_case fallback || die_env 'fallback case setup failed'; MARKER=$CASE_DIR/expected.marker; marker fallback "$MARKER" && cp -- "$MARKER" "$CASE_TARGET" && cp -- "$MARKER" "$CASE_DIR/input (converted).$TARGET_EXT" || die_env 'fallback marker setup failed'; post_convert fallback "$CASE_SOURCE" 'auto_rename=true' || die_env 'fallback curl/service failure'; [ "$POST_CODE" = 200 ] && marker_unchanged "$MARKER" "$CASE_TARGET" && marker_unchanged "$MARKER" "$CASE_DIR/input (converted).$TARGET_EXT" && nonempty_regular "$CASE_DIR/input (converted 2).$TARGET_EXT" && pass 'auto-rename candidate fallback' || fail 'auto-rename fallback assertion failed'
copy_case exhausted || die_env 'exhaustion case setup failed'; MARKER=$CASE_DIR/expected.marker; marker exhausted "$MARKER" && cp -- "$MARKER" "$CASE_TARGET" || die_env 'exhaustion marker setup failed'; i=1; while [ "$i" -le 10 ]; do suffix=' (converted)'; [ "$i" -eq 1 ] || suffix=" (converted $i)"; cp -- "$MARKER" "$CASE_DIR/input$suffix.$TARGET_EXT" || die_env 'candidate exhaustion setup failed'; i=$((i + 1)); done; post_convert exhausted "$CASE_SOURCE" 'auto_rename=true' || die_env 'exhaustion curl/service failure'; intact=1; marker_unchanged "$MARKER" "$CASE_TARGET" || intact=0; i=1; while [ "$i" -le 10 ]; do suffix=' (converted)'; [ "$i" -eq 1 ] || suffix=" (converted $i)"; marker_unchanged "$MARKER" "$CASE_DIR/input$suffix.$TARGET_EXT" || intact=0; i=$((i + 1)); done; [ "$POST_CODE" = 409 ] && [ "$intact" -eq 1 ] && pass 'auto-rename exhaustion returns 409 and preserves all markers' || fail 'auto-rename exhaustion assertion failed'
copy_case invalid || die_env 'invalid-flags case setup failed'; post_convert invalid "$CASE_SOURCE" 'overwrite=true&auto_rename=true' || die_env 'invalid-flags curl/service failure'; [ "$POST_CODE" = 400 ] && pass 'invalid combined flags return 400' || fail 'invalid-flags assertion failed'
copy_case concurrent || die_env 'concurrent case setup failed'; start_post concurrent-a "$CASE_SOURCE" '' || die_env 'concurrent request setup failed'; pid_a=$CURL_PID; start_post concurrent-b "$CASE_SOURCE" '' || die_env 'concurrent request setup failed'; pid_b=$CURL_PID; wait_curl "$pid_a"; rc_a=$?; wait_curl "$pid_b"; rc_b=$?; code_a=$(read_code "$RESP_DIR/concurrent-a.code"); code_b=$(read_code "$RESP_DIR/concurrent-b.code"); if [ "$rc_a" -eq 0 ] && [ "$rc_b" -eq 0 ] && { { [ "$code_a" = 200 ] && [ "$code_b" = 409 ]; } || { [ "$code_a" = 409 ] && [ "$code_b" = 200 ]; }; } && nonempty_regular "$CASE_TARGET"; then pass "concurrent requests returned exactly one 200 and one 409 ($code_a/$code_b)"; else fail "concurrent requests require exactly one 200 and one 409; got $code_a/$code_b"; fi

if [ "$RACE_ROUNDS" -gt 0 ]; then
    command -v python3 >/dev/null 2>&1 || die_env 'python3 is required when --race-rounds is non-zero'
    round=1
    while [ "$round" -le "$RACE_ROUNDS" ]; do
        copy_case "race-$round" || die_env 'race case setup failed'; MARKER=$CASE_DIR/external.marker; marker "race-$round" "$MARKER" || die_env 'race marker setup failed'; start_post "race-$round" "$CASE_SOURCE" '' || die_env 'race request setup failed'; pid=$CURL_PID
        python3 -I -S - "$CASE_TARGET" "$MARKER" <<'PY' >/dev/null 2>&1
import os, sys
target, marker = sys.argv[1:]
fd = None
try:
    with open(marker, 'rb') as marker_file:
        data = marker_file.read()
    try:
        fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        raise SystemExit(1)
    except BaseException:
        raise SystemExit(2)
    offset = 0
    try:
        while offset < len(data):
            written = os.write(fd, data[offset:])
            if not isinstance(written, int) or written <= 0 or written > len(data) - offset:
                raise OSError('short write')
            offset += written
        os.fsync(fd)
    except BaseException:
        try:
            os.close(fd)
        except BaseException:
            pass
        fd = None
        raise SystemExit(3)
    try:
        os.close(fd)
    except BaseException:
        fd = None
        raise SystemExit(4)
    fd = None
except SystemExit:
    raise
except BaseException:
    raise SystemExit(2)
finally:
    if fd is not None:
        try:
            os.close(fd)
        except BaseException:
            pass
PY
        creator_rc=$?; wait_curl "$pid"; request_rc=$?; code=$(read_code "$RESP_DIR/race-$round.code")
        [ "$request_rc" -eq 0 ] || die_env 'diagnostic race request curl/service failure'
        case $creator_rc in 0|1) ;; *) fail "diagnostic O_EXCL creator failed in round $round"; round=$((round + 1)); continue ;; esac
        case $code in 200|409) ;; *) fail "diagnostic race received unexpected HTTP status in round $round"; round=$((round + 1)); continue ;; esac
        if [ "$creator_rc" -eq 0 ]; then
            if [ "$code" = 409 ] && marker_unchanged "$MARKER" "$CASE_TARGET"; then warn "OBSERVED: race round $round is compatible with marker preservation; it does not prove atomic publication"; else fail "race round $round creator won but HTTP/marker integrity was unsafe"; fi
        else warn "race round $round did not acquire marker; no atomicity proof was observed"; fi
        round=$((round + 1))
    done
fi

note "SUMMARY: failures=$FAILURES warnings=$WARNINGS (all artifacts retained; no paths printed)"
[ "$FAILURES" -eq 0 ] || exit 1
exit 0
