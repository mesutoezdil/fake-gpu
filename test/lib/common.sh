# shellcheck shell=bash
# Helpers sourced by the bash checks under test/.
#
#   source "$ROOT/test/lib/common.sh"
#
# Every helper fails loudly: a check never passes because a command was
# skipped, returned nothing, or matched the wrong thing.

pass() { printf 'PASS: %s\n' "$*"; }

# fail MESSAGE [DETAIL...]: print the message and any detail, then exit 1.
fail() {
    printf 'FAIL: %s\n' "$1" >&2
    shift
    if [ "$#" -gt 0 ]; then printf '%s\n' "$@" | sed 's/^/    | /' >&2; fi
    exit 1
}

# has FILE TEXT / lacks FILE TEXT: fixed-string checks on a rendered file.
has() { grep -Fq -- "$2" "$1" || fail "$1 does not contain: $2"; }
lacks() { if grep -Fq -- "$2" "$1"; then fail "$1 unexpectedly contains: $2"; fi; }

# expect OUTPUT TEXT LABEL: OUTPUT contains TEXT, then report LABEL.
expect() {
    grep -Fq -- "$2" <<< "$1" || fail "expected '$2'" "$1"
    pass "$3"
}

# shows TEXT COMMAND...: COMMAND succeeds and its output contains TEXT. The
# output is captured first, so an early grep exit cannot break the pipe.
shows() {
    local text="$1" out
    shift
    out=$("$@" 2>&1) || return 1
    grep -Fq -- "$text" <<< "$out"
}

# eventually SECONDS COMMAND...: rerun COMMAND once a second until it
# succeeds. Returns 1 after SECONDS. COMMAND runs without errexit, so it must
# report failure through its exit status.
eventually() {
    local deadline=$((SECONDS + $1))
    shift
    until "$@"; do
        [ "$SECONDS" -lt "$deadline" ] || return 1
        sleep 1
    done
}

# is_int VALUE: VALUE is a non-negative decimal integer.
is_int() { [[ "$1" =~ ^[0-9]+$ ]]; }
