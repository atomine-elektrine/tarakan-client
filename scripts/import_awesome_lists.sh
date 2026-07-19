#!/usr/bin/env bash
# Harvest individual project repos linked from curated awesome-list READMEs
# and register them with Tarakan.
#
# Example markdown (awesome-go style):
#   - [beep](https://github.com/gopxl/beep) - playback library
#   - [casbin](https://github.com/hsluoyz/casbin) - authz
#
# Usage:
#   ./scripts/import_awesome_lists.sh avelino/awesome-go --max 100
#   ./scripts/import_awesome_lists.sh vinta/awesome-python --max 50
#   ./scripts/import_awesome_lists.sh --file repos.txt
#   ./scripts/import_awesome_lists.sh avelino/awesome-go --max 20 --scan --agent grok
#
# Prerequisites: tarakan binary, tarakan login (or TARAKAN_URL + TARAKAN_API_TOKEN).
# Optional: GITHUB_TOKEN for higher GitHub rate limits.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLIENT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

TARAKAN_BIN="${TARAKAN_BIN:-}"
if [[ -z "${TARAKAN_BIN}" ]]; then
  if [[ -x "${CLIENT_ROOT}/bin/tarakan" ]]; then
    TARAKAN_BIN="${CLIENT_ROOT}/bin/tarakan"
  elif command -v tarakan >/dev/null 2>&1; then
    TARAKAN_BIN="$(command -v tarakan)"
  else
    echo "error: tarakan binary not found; build with: make -C ${CLIENT_ROOT} build" >&2
    exit 1
  fi
fi

WORK_DIR="${TARAKAN_IMPORT_DIR:-${TMPDIR:-/tmp}/tarakan-awesome-import}"
LISTS_FILE=""
REPOS_FILE=""
MAX_REPOS=500
SLEEP_MS=300
DO_REGISTER=1
DO_SCAN=0
AGENT="${TARAKAN_AGENT:-}"
# Drop other curated-list indexes (awesome-*). Keep real projects.
DROP_META_LISTS=1
EXTRA_LISTS=()

usage() {
  cat >&2 <<'EOF'
Usage: import_awesome_lists.sh [options] [curated-list owner/name ...]

Pulls GitHub project links from a curated list README (e.g. avelino/awesome-go)
and registers those individual projects with Tarakan — not the list itself.

Examples:
  ./scripts/import_awesome_lists.sh avelino/awesome-go --max 100
  ./scripts/import_awesome_lists.sh vinta/awesome-python --max 50 --scan --agent grok
  ./scripts/import_awesome_lists.sh --file /tmp/tarakan-awesome-import/repos.txt

Options:
  --lists FILE       File of curated-list owner/name lines
  --file FILE        Skip harvest; register owner/name lines from FILE
  --max N            Max project repos to keep (default 500)
  --sleep-ms N       Delay between register calls (default 300)
  --keep-meta-lists  Keep awesome-* list indexes too (default: drop them)
  --scan             After register: tarakan worker --agent AGENT --once
  --agent NAME       Agent for --scan (or $TARAKAN_AGENT)
  --no-register      Only write repos.txt
  -h, --help

Env: TARAKAN_URL, TARAKAN_API_TOKEN, GITHUB_TOKEN, TARAKAN_BIN, TARAKAN_IMPORT_DIR
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --lists) LISTS_FILE="${2:?}"; shift 2 ;;
    --file) REPOS_FILE="${2:?}"; shift 2 ;;
    --max) MAX_REPOS="${2:?}"; shift 2 ;;
    --sleep-ms) SLEEP_MS="${2:?}"; shift 2 ;;
    --keep-meta-lists) DROP_META_LISTS=0; shift ;;
    --scan) DO_SCAN=1; shift ;;
    --agent) AGENT="${2:?}"; shift 2 ;;
    --no-register) DO_REGISTER=0; shift ;;
    -h|--help) usage; exit 0 ;;
    --) shift; break ;;
    -*)
      echo "unknown option: $1" >&2
      usage
      exit 2
      ;;
    *)
      EXTRA_LISTS+=("$1")
      shift
      ;;
  esac
done

mkdir -p "${WORK_DIR}"
HARVESTED="${WORK_DIR}/repos.txt"
LISTS_OUT="${WORK_DIR}/lists.txt"
: >"${LISTS_OUT}"

if [[ -n "${LISTS_FILE}" ]]; then
  cat "${LISTS_FILE}" >>"${LISTS_OUT}"
fi
for list in "${EXTRA_LISTS[@]+"${EXTRA_LISTS[@]}"}"; do
  printf '%s\n' "${list}" >>"${LISTS_OUT}"
done

say() { printf '%s\n' "$*" >&2; }

normalize_slug() {
  local input="$1"
  input="${input%%#*}"
  input="$(printf '%s' "${input}" | tr -d '\r' | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
  [[ -z "${input}" || "${input}" == \#* ]] && return 1
  input="${input#https://}"
  input="${input#http://}"
  input="${input#www.}"
  input="${input#github.com/}"
  input="${input%.git}"
  input="${input%%\?*}"
  # owner/name only (drop /tree/... /blob/...)
  if [[ "${input}" =~ ^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)(/|$) ]]; then
    printf '%s/%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}"
    return 0
  fi
  return 1
}

fetch_readme() {
  local slug="$1"
  local owner name url body
  owner="${slug%%/*}"
  name="${slug#*/}"

  local api_headers=(-H "Accept: application/vnd.github.raw+json" -H "User-Agent: tarakan-import-awesome")
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    api_headers+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  fi
  url="https://api.github.com/repos/${owner}/${name}/readme"
  if body="$(curl -fsSL --max-time 60 "${api_headers[@]}" "${url}" 2>/dev/null)" && [[ -n "${body}" ]]; then
    printf '%s\n' "${body}"
    return 0
  fi

  local branch file
  for branch in main master; do
    for file in README.md readme.md Readme.md; do
      url="https://raw.githubusercontent.com/${owner}/${name}/${branch}/${file}"
      if body="$(curl -fsSL --max-time 30 -H "User-Agent: tarakan-import-awesome" "${url}" 2>/dev/null)" && [[ -n "${body}" ]]; then
        printf '%s\n' "${body}"
        return 0
      fi
    done
  done
  return 1
}

# From: - [beep](https://github.com/gopxl/beep) - desc
# Also matches /tree/ and /blob/ deep links → owner/name only.
extract_repos() {
  grep -oE 'github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+' \
    | sed -E 's#^github\.com/##' \
    | sed -E 's/\.git$//' \
    | tr '[:upper:]' '[:lower:]' \
    | grep -E '^[a-z0-9][a-z0-9_.-]*/[a-z0-9._-]+$' \
    | grep -vE '^(topics|settings|orgs|marketplace|sponsors|features|pricing|about|login|join|site|apps)/' \
    || true
}

# Meta indexes to drop (the list pages, not libraries).
is_meta_list_repo() {
  local slug="$1"
  local name="${slug#*/}"
  case "${name}" in
    awesome|awesome-*|*awesome*|*awesome|*-list|*-lists|bookmarks|*-bookmarks)
      return 0
      ;;
  esac
  return 1
}

filter_projects() {
  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -z "${line}" ]] && continue
    if [[ "${DROP_META_LISTS}" -eq 1 ]] && is_meta_list_repo "${line}"; then
      continue
    fi
    printf '%s\n' "${line}"
  done
}

if [[ -n "${REPOS_FILE}" ]]; then
  say "using pre-built repo list: ${REPOS_FILE}"
  cp "${REPOS_FILE}" "${HARVESTED}"
else
  if [[ ! -s "${LISTS_OUT}" ]]; then
    say "error: pass a curated list like avelino/awesome-go, or --file repos.txt"
    usage
    exit 2
  fi

  say "harvesting project links from curated lists → ${HARVESTED}"
  : >"${HARVESTED}.raw"
  while IFS= read -r raw || [[ -n "${raw}" ]]; do
    slug="$(normalize_slug "${raw}" || true)"
    [[ -z "${slug}" ]] && continue
    say "  list ${slug}"
    if ! readme="$(fetch_readme "${slug}")"; then
      say "  warn: could not fetch README for ${slug}"
      continue
    fi
    printf '%s\n' "${readme}" | extract_repos >>"${HARVESTED}.raw"
  done <"${LISTS_OUT}"

  sort -u "${HARVESTED}.raw" >"${HARVESTED}.sorted"

  # Never register the curated list repo itself.
  while IFS= read -r raw || [[ -n "${raw}" ]]; do
    slug="$(normalize_slug "${raw}" || true)"
    [[ -z "${slug}" ]] && continue
    grep -viF -x "${slug}" "${HARVESTED}.sorted" >"${HARVESTED}.tmp" 2>/dev/null || true
    mv "${HARVESTED}.tmp" "${HARVESTED}.sorted"
  done <"${LISTS_OUT}"

  filter_projects <"${HARVESTED}.sorted" >"${HARVESTED}.projects"
  head -n "${MAX_REPOS}" "${HARVESTED}.projects" >"${HARVESTED}"

  raw_count="$(wc -l <"${HARVESTED}.sorted" | tr -d ' ')"
  project_count="$(wc -l <"${HARVESTED}.projects" | tr -d ' ')"
  kept="$(wc -l <"${HARVESTED}" | tr -d ' ')"
  rm -f "${HARVESTED}.raw" "${HARVESTED}.sorted" "${HARVESTED}.projects" "${HARVESTED}.tmp"

  say "unique github links: ${raw_count}"
  say "after dropping meta lists: ${project_count}"
  say "keeping: ${kept} (max ${MAX_REPOS}) → ${HARVESTED}"
  say "sample projects:"
  head -n 10 "${HARVESTED}" | while IFS= read -r line; do
    say "  ${line}"
  done
fi

count="$(wc -l <"${HARVESTED}" | tr -d ' ')"
if [[ "${count}" -eq 0 ]]; then
  say "error: no project repositories harvested."
  exit 1
fi

if [[ "${DO_REGISTER}" -eq 1 ]]; then
  say "registering ${count} projects with ${TARAKAN_BIN}…"
  "${TARAKAN_BIN}" register --file "${HARVESTED}" --sleep-ms "${SLEEP_MS}"
fi

if [[ "${DO_SCAN}" -eq 1 ]]; then
  if [[ -z "${AGENT}" ]]; then
    say "error: --scan requires --agent or TARAKAN_AGENT"
    exit 2
  fi
  say "worker --agent ${AGENT} --once"
  "${TARAKAN_BIN}" worker --agent "${AGENT}" --once
fi

say "done. project list: ${HARVESTED}"
