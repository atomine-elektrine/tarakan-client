#!/usr/bin/env bash
# Install tarakan (tarakan-client) the easy way.
#   curl -fsSL https://raw.githubusercontent.com/atomine-elektrine/tarakan-client/main/install.sh | bash
# Or from a Tarakan host:
#   curl -fsSL https://your.tarakan.host/install.sh | bash
set -euo pipefail

REPO="${TARAKAN_REPO:-atomine-elektrine/tarakan-client}"
BIN_NAME="tarakan"
INSTALL_DIR="${TARAKAN_INSTALL_DIR:-${HOME}/.local/bin}"

say() { printf '%s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "need '$1' on PATH"
}

detect_os() {
  case "$(uname -s)" in
    Linux*) echo linux ;;
    Darwin*) echo darwin ;;
    MINGW*|MSYS*|CYGWIN*) echo windows ;;
    *) die "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
}

latest_tag() {
  need_cmd curl
  # Prefer gh when available; otherwise unauthenticated API.
  if command -v gh >/dev/null 2>&1; then
    gh release view --repo "${REPO}" --json tagName -q .tagName 2>/dev/null && return
  fi
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' \
    | head -n1
}

install_from_release() {
  local os arch tag version archive url tmp dir binary
  os="$(detect_os)"
  arch="$(detect_arch)"
  tag="$(latest_tag)"
  [[ -n "${tag}" ]] || return 1
  version="${tag//\//-}"
  if [[ "${os}" == "windows" ]]; then
    archive="tarakan_${version}_${os}_${arch}.zip"
  else
    archive="tarakan_${version}_${os}_${arch}.tar.gz"
  fi
  url="https://github.com/${REPO}/releases/download/${tag}/${archive}"
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp}"' RETURN

  say "downloading ${url}"
  curl -fsSL "${url}" -o "${tmp}/${archive}" || return 1

  mkdir -p "${INSTALL_DIR}"
  if [[ "${os}" == "windows" ]]; then
    need_cmd unzip
    unzip -q "${tmp}/${archive}" -d "${tmp}/out"
    binary="$(find "${tmp}/out" -type f -name 'tarakan.exe' | head -n1)"
    [[ -n "${binary}" ]] || return 1
    install -m 755 "${binary}" "${INSTALL_DIR}/tarakan.exe"
  else
    need_cmd tar
    tar -xzf "${tmp}/${archive}" -C "${tmp}"
    binary="$(find "${tmp}" -type f -name tarakan | head -n1)"
    [[ -n "${binary}" ]] || return 1
    install -m 755 "${binary}" "${INSTALL_DIR}/${BIN_NAME}"
  fi
  say "installed ${INSTALL_DIR}/${BIN_NAME}"
  return 0
}

install_with_go() {
  need_cmd go
  say "no release binary for this platform; using go install"
  GOBIN="${INSTALL_DIR}" go install "github.com/${REPO}/cmd/tarakan@latest"
  say "installed ${INSTALL_DIR}/${BIN_NAME}"
}

path_hint() {
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      say ""
      say "add to PATH:"
      say "  export PATH=\"${INSTALL_DIR}:\$PATH\""
      ;;
  esac
  say ""
  say "next:"
  say "  tarakan login"
  say "  tarakan report --agent grok --pickup"
}

main() {
  need_cmd uname
  need_cmd curl
  mkdir -p "${INSTALL_DIR}"

  if install_from_release; then
    path_hint
    exit 0
  fi

  if command -v go >/dev/null 2>&1; then
    install_with_go
    path_hint
    exit 0
  fi

  die "could not download a release for $(detect_os)/$(detect_arch) and Go is not installed.
Publish a release tag (v*) on ${REPO}, or install Go and re-run."
}

main "$@"
