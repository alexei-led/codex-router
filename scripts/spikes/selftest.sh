#!/usr/bin/env bash
# Offline check of the spike tooling. Runs no codex, needs no network.
set -euo pipefail

# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

handle_help selftest.sh "Checks the spike tooling offline: bash -n and shellcheck on every script, then
shape-proxy.py against a fake upstream (forwarding, rewrite, faults, 60-char redaction), the
app-server client against a fake newline JSON-RPC server, and that the fake secrets
secret-XYZ / acct-secret-ABC appear in no output or record file." "$@"

need python3
RT=$(make_rt cr-spike-self)
failed=0

fail() {
	echo "FAIL $*"
	failed=1
}

echo "== selftest: evidence dir $RT"

for f in "$SPIKE_DIR"/*.sh; do
	if bash -n "$f" 2>"$RT/syntax.err"; then
		echo "PASS bash -n $(basename "$f")"
	else
		fail "bash -n $(basename "$f")"
		cat "$RT/syntax.err"
	fi
done
if [ -x /bin/bash ] && /bin/bash -n "$SPIKE_DIR/lib.sh" 2>/dev/null; then
	echo "PASS /bin/bash -n lib.sh"
else
	fail "/bin/bash -n lib.sh"
fi

for f in "$SPIKE_DIR"/*.py; do
	if python3 -I -c 'import sys; compile(open(sys.argv[1]).read(), sys.argv[1], "exec")' "$f"; then
		echo "PASS compile $(basename "$f")"
	else
		fail "compile $(basename "$f")"
	fi
done

if command -v shellcheck >/dev/null 2>&1; then
	if shellcheck -x -P "$SPIKE_DIR" "$SPIKE_DIR"/*.sh; then
		echo "PASS shellcheck"
	else
		fail "shellcheck"
	fi
else
	echo "SKIP shellcheck (not installed)"
fi

python3 -I "$SPIKE_DIR/selftest-driver.py" "$RT" "$SHAPE_PROXY" "$APPSERVER_CLIENT" | tee "$RT/driver.out" || failed=1
grep -q '^FAIL' "$RT/driver.out" && failed=1

if grep -rIl -e 'secret-XYZ' -e 'acct-secret-ABC' "$RT" >"$RT/leaks.txt" 2>/dev/null && [ -s "$RT/leaks.txt" ]; then
	fail "secret value found in: $(tr '\n' ' ' <"$RT/leaks.txt")"
else
	echo "PASS secret values absent from every output and record file"
fi

echo
if [ "$failed" = 0 ]; then
	echo "selftest: all checks passed"
else
	echo "selftest: FAILED"
fi
echo "evidence: $RT"
exit "$failed"
