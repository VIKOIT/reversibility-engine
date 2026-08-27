#!/usr/bin/env bash
#
# probe-dialect.sh — report how the engine classifies SQL it is shown.
#
# It ANALYZES ONLY. It writes nothing outside its own temporary directory, connects to nothing,
# and changes no file in the repository. It exists so that a claim about what the engine does
# with a construct is measured rather than asserted — the "Today" column of
# docs/proposals/pg-dialect-triage.md was produced by this script.
#
# Two modes:
#
#   probe-dialect.sh statements.txt       one SQL statement per line; prints a verdict per line
#   probe-dialect.sh --corpus DIR         every *.sql under DIR; counts verdicts by rule ID
#
# The second is the one to run against the dialect corpus when the harness resumes: it answers
# "which constructs are actually reaching PG027, and how often", which is the ranking the
# proposal could not produce without it.

set -euo pipefail

die() { printf 'probe-dialect: %s\n' "$1" >&2; exit 2; }

REVCTL="${REVCTL:-}"
if [ -z "$REVCTL" ]; then
    REVCTL="$(mktemp -d)/revctl"
    go build -o "$REVCTL" ./cmd/revctl || die 'could not build revctl'
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# The engine emits JSON; jq is not available everywhere this runs, and a dependency for four
# field reads is not worth it.
read_field() {
    perl -0777 -ne '
        my ($grade) = /"grade":\s*"([^"]*)"/;
        my @rules   = /"ruleId":\s*"([^"]*)"/g;
        my @revs    = /"reversibility":\s*"([^"]*)"/g;
        my @locks   = /"lockHazard":\s*"([^"]*)"/g;
        printf("%s|%s|%s|%s", $grade // "?", join(",",@rules) || "-",
               join(",",@revs) || "-", join(",",@locks) || "-");
    '
}

# certify runs one .sql file and prints "grade|rules|reversibilities|locks".
#
# A down migration is supplied so that the missing-down cap (PG/DOWN001) does not colour every
# verdict C and hide the classification being probed.
certify() {
    local sql="$1" dir="$WORK/run"

    rm -rf "$dir"; mkdir -p "$dir"
    cp "$sql" "$dir/0001_probe.up.sql"
    printf 'SELECT 1;\n' > "$dir/0001_probe.down.sql"

    "$REVCTL" check --no-config --format json "$dir" 2>/dev/null | read_field
}

statements_mode() {
    local file="$1" n=0
    [ -f "$file" ] || die "no such file: $file"

    printf '%-3s %-5s %-9s %-12s %-14s %s\n' '#' 'GRADE' 'RULE' 'REVERSIBILITY' 'LOCK' 'STATEMENT'
    while IFS= read -r stmt; do
        case "$stmt" in ''|'#'*) continue ;; esac
        n=$((n + 1))

        printf '%s\n' "$stmt" > "$WORK/one.sql"
        IFS='|' read -r grade rules revs locks <<< "$(certify "$WORK/one.sql")"

        printf '%-3s %-5s %-9s %-12s %-14s %s\n' "$n" "$grade" "$rules" "$revs" "$locks" "$stmt"
    done < "$file"
}

corpus_mode() {
    local dir="$1" total=0
    [ -d "$dir" ] || die "no such directory: $dir"

    : > "$WORK/tally"

    while IFS= read -r sql; do
        total=$((total + 1))
        IFS='|' read -r _ rules _ _ <<< "$(certify "$sql")"

        # One line per rule ID that fired, so the tally counts occurrences and not files.
        printf '%s\n' "$rules" | tr ',' '\n' >> "$WORK/tally"

        # The unclassified ones are the point of the exercise, so keep their sources listed.
        case "$rules" in *PG027*) printf '%s\n' "$sql" >> "$WORK/unknown-files" ;; esac
    done < <(find "$dir" -type f -name '*.sql' | sort)

    printf 'Analyzed %s file(s) under %s\n\n' "$total" "$dir"
    printf '%-8s %s\n' 'COUNT' 'RULE'
    sort "$WORK/tally" | grep -v '^-\?$' | uniq -c | sort -rn |
        while read -r count rule; do printf '%-8s %s\n' "$count" "$rule"; done

    if [ -s "${WORK}/unknown-files" ]; then
        printf '\nFiles reaching PG027 (unclassified — this is the ranking input):\n'
        sort -u "$WORK/unknown-files"
    fi
}

case "${1:-}" in
    --corpus) [ $# -eq 2 ] || die 'usage: probe-dialect.sh --corpus DIR'; corpus_mode "$2" ;;
    '')       die 'usage: probe-dialect.sh STATEMENTS.txt | --corpus DIR' ;;
    *)        statements_mode "$1" ;;
esac
