#!/bin/sh
# Runs the iOS tests the way CI does, with a self-explaining failure:
# every phase's outcome — and on failure its output tail — goes to
# stdout and, under GitHub Actions, to the step summary, which the run
# page shows without authentication. A red run should say why.
#
# Phases: bind the framework, generate the project, pick a simulator,
# run the tests, leave the simulator as it was found.
set -u
cd "$(dirname "$0")/.."
mkdir -p .build

SUMMARY="${GITHUB_STEP_SUMMARY:-}"

note() {
	printf '%s\n' "$1"
	if [ -n "$SUMMARY" ]; then
		printf '%s\n\n' "$1" >> "$SUMMARY" || true
	fi
}

# dump <logfile> — on failure, show the tail where a person can read it.
dump() {
	logfile=$1
	if [ -n "$SUMMARY" ]; then
		{
			echo '```'
			tail -60 "$logfile" 2>/dev/null
			echo '```'
		} >> "$SUMMARY" || true
	else
		tail -60 "$logfile" 2>/dev/null
	fi
}

note "## make ios-test"
note "- xcodebuild: $(xcodebuild -version 2>&1 | head -1)"
note "- xcodegen: $(xcodegen --version 2>&1 | head -1)"

note "### bind the framework"
if ! make ios-framework > .build/ios-framework.log 2>&1; then
	note "FAILED"
	dump .build/ios-framework.log
	exit 1
fi
note "ok"

note "### generate the project"
if ! xcodegen generate > .build/xcodegen.log 2>&1; then
	note "FAILED"
	dump .build/xcodegen.log
	exit 1
fi
note "ok"

# Pick a concrete simulator: prefer an iPhone that is not already booted
# (this machine runs other work), fall back to any iPhone, and say so.
UDID=$(xcrun simctl list devices available 2>/dev/null | awk -F'[()]' '/iPhone/ && !/Booted/{ print $2; exit }')
if [ -z "$UDID" ]; then
	UDID=$(xcrun simctl list devices available 2>/dev/null | awk -F'[()]' '/iPhone/{ print $2; exit }')
fi
if [ -z "$UDID" ]; then
	note "### no iOS simulator is available"
	note '```'
	xcrun simctl list devices available 2>&1 | head -40 >> "$SUMMARY" 2>/dev/null || xcrun simctl list devices available 2>&1 | head -40
	note '```'
	exit 1
fi
WAS_BOOTED=$(xcrun simctl list devices 2>/dev/null | awk -F'[()]' -v u="$UDID" 'index($0,u) && /Booted/{print "y"; exit}')
note "### tests on iOS simulator $UDID (was booted before: ${WAS_BOOTED:-no})"

LOG=.build/ios-test-output.log
xcodebuild -project Synk.xcodeproj -scheme Synk-iOS \
	-destination "id=$UDID" test CODE_SIGNING_ALLOWED=NO \
	-derivedDataPath .build/ios > "$LOG" 2>&1
STATUS=$?
if [ "$STATUS" -ne 0 ]; then
	note "xcodebuild exited $STATUS — last 60 lines:"
	dump "$LOG"
else
	note "ok"
fi

if [ -z "$WAS_BOOTED" ]; then
	xcrun simctl shutdown "$UDID" >/dev/null 2>&1 || true
fi
exit "$STATUS"
