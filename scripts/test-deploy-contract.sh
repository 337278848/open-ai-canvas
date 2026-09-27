#!/usr/bin/env bash

set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

fail_test() {
    printf 'deployment script test failed: %s\n' "$1" >&2
    exit 1
}

source_without_main() {
    local script="$1"
    # The installers are intentionally sourced only after removing their entrypoint.
    # This exercises pure preflight/helpers without installing packages or starting services.
    source <(sed '/^main "\$@"/d' "$script")
}

assert_file_is_lf() {
    local path="$1"
    LC_ALL=C grep -q $'\r' "$path" && fail_test "$path contains CR characters"
    [[ "$(tail -c 1 "$path" | od -An -t x1 | tr -d ' \n')" == "0a" ]] ||
        fail_test "$path does not end with LF"
}

for script in \
    "$ROOT/scripts/install-server.sh" \
    "$ROOT/scripts/install-server-image.sh" \
    "$ROOT/scripts/install-host-updater.sh"; do
    assert_file_is_lf "$script"
done

(
    source_without_main "$ROOT/scripts/install-server.sh"
    directory="$(mktemp -d)"
    trap 'rm -rf "$directory"' EXIT
    printf 'POSTGRES_PASSWORD=test-secret' >"$directory/.env"
    set_env_values "$directory/.env" \
        CANVAS_BACKEND_IMAGE "open-ai-canvas-backend:server" \
        CANVAS_WEB_IMAGE "open-ai-canvas-web:server"
    expected=$'POSTGRES_PASSWORD=test-secret\nCANVAS_BACKEND_IMAGE=open-ai-canvas-backend:server\nCANVAS_WEB_IMAGE=open-ai-canvas-web:server\n'
    printf '%s' "$expected" >"$directory/expected"
    cmp -s "$directory/expected" "$directory/.env" ||
        fail_test "atomic env update did not add a separator to a file without final LF"
)

mixed_directory="$(mktemp -d)"
mixed_bin="$mixed_directory/bin"
mkdir -p "$mixed_bin"
cat >"$mixed_bin/docker" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$mixed_bin/docker"
cat >"$mixed_directory/.env" <<'EOF'
CANVAS_IMAGE_TAG=1.5.7.2
CANVAS_BACKEND_IMAGE=ghcr.io/ddcat-ai/open-ai-canvas-backend@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
CANVAS_WEB_IMAGE=ghcr.io/ddcat-ai/open-ai-canvas-web:1.5.7.2
EOF
cat >"$mixed_directory/docker-compose.deploy.yml" <<'EOF'
services:
  migrate:
    image: ghcr.io/ddcat-ai/open-ai-canvas-backend:${CANVAS_IMAGE_TAG:-latest}
  backend:
    image: ghcr.io/ddcat-ai/open-ai-canvas-backend:${CANVAS_IMAGE_TAG:-latest}
  web:
    image: ghcr.io/ddcat-ai/open-ai-canvas-web:${CANVAS_IMAGE_TAG:-latest}
EOF
if (
    source_without_main "$ROOT/scripts/install-host-updater.sh"
    INSTALL_DIR="$mixed_directory"
    PATH="$mixed_bin:$PATH"
    prepare_deployment_contract
); then
    rm -rf "$mixed_directory"
    fail_test "mixed digest/tag deployment passed without a local digest"
fi
grep -Fq 'CANVAS_IMAGE_TAG:-latest' "$mixed_directory/docker-compose.deploy.yml" ||
    fail_test "mixed digest/tag preflight rewrote Compose before failing"
grep -Fq 'CANVAS_WEB_IMAGE=ghcr.io/ddcat-ai/open-ai-canvas-web:1.5.7.2' "$mixed_directory/.env" ||
    fail_test "mixed digest/tag preflight rewrote .env before failing"
rm -rf "$mixed_directory"

(
    source_without_main "$ROOT/scripts/install-server-image.sh"
    REPOSITORY_REF="upstream/main"
    CANVAS_IMAGE_TAG=""
    RELEASE_REF=""
    COMPOSE_REF=""
    COMPOSE_URL=""
    UPDATER_INSTALL_URL=""
    resolve_release_ref
    [[ "$RELEASE_REF" == "upstream/main" ]] ||
        fail_test "upstream/main was not accepted as a release ref"
)

(
    source_without_main "$ROOT/scripts/install-server-image.sh"
    IMAGE_REPOSITORY="ghcr.io/acme/canvas"
    validate_image_repository
    validate_image_digest \
        "ghcr.io/acme/canvas-backend@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
        backend
)

(
    source_without_main "$ROOT/scripts/install-host-updater.sh"
    REPOSITORY="acme/canvas"
    IMAGE_REPOSITORY=""
    [[ "$(image_repository)" == "ghcr.io/acme/canvas" ]] ||
        fail_test "updater image repository was hard-coded to open-ai-canvas"
)

(
    source_without_main "$ROOT/scripts/install-server-image.sh"
    REPOSITORY_REF="main"
    CANVAS_IMAGE_TAG="1.5.7.1"
    RELEASE_REF=""
    COMPOSE_REF=""
    COMPOSE_URL=""
    UPDATER_INSTALL_URL=""
    resolve_release_ref
    [[ "$RELEASE_REF" == "v1.5.7.1" ]] ||
        fail_test "image tag did not determine the release ref"
)

(
    source_without_main "$ROOT/scripts/install-server-image.sh"
    REPOSITORY_REF="main"
    CANVAS_IMAGE_TAG="1.5.7.1"
    RELEASE_REF="upstream/main"
    COMPOSE_REF="upstream/main"
    COMPOSE_URL=""
    UPDATER_INSTALL_URL=""
    resolve_release_ref
    [[ "$RELEASE_REF" == "upstream/main" && "$COMPOSE_REF" == "upstream/main" ]] ||
        fail_test "consistent explicit release refs were not preserved"
)

if (
    source_without_main "$ROOT/scripts/install-server-image.sh"
    REPOSITORY_REF="main"
    CANVAS_IMAGE_TAG="1.5.7.1"
    RELEASE_REF="main"
    COMPOSE_REF=""
    COMPOSE_URL=""
    UPDATER_INSTALL_URL=""
    resolve_release_ref
); then
    fail_test "mismatched image tag and release ref were accepted"
fi

for legacy in 1.2.9 1.5.7 1.5.7.1; do
    legacy_directory="$(mktemp -d)"
    if (
        source_without_main "$ROOT/scripts/install-server-image.sh"
        INSTALL_DIR="$legacy_directory"
        REPOSITORY_REF="main"
        CANVAS_IMAGE_TAG="$legacy"
        RELEASE_REF=""
        COMPOSE_REF=""
        COMPOSE_URL=""
        UPDATER_INSTALL_URL=""
        guard_legacy_release_before_write
    ); then
        rm -rf "$legacy_directory"
        fail_test "image installer accepted legacy release $legacy"
    fi
    [[ -z "$(find "$legacy_directory" -mindepth 1 -maxdepth 1 -print -quit)" ]] ||
        fail_test "image installer wrote before rejecting legacy release $legacy"
    rm -rf "$legacy_directory"
done

(
    source_without_main "$ROOT/scripts/install-server-image.sh"
    REPOSITORY_REF="main"
    CANVAS_IMAGE_TAG="1.5.7.1"
    RELEASE_REF=""
    COMPOSE_REF=""
    COMPOSE_URL=""
    UPDATER_INSTALL_URL=""
    CANVAS_SKIP_HOST_UPDATER=1
    guard_legacy_release_before_write
    [[ "$LEGACY_RELEASE_SKIP" == "1" ]] ||
        fail_test "explicit legacy updater skip was not recorded"
)

for legacy in 1.2.9 1.5.7 1.5.7.1; do
    if (
        source_without_main "$ROOT/scripts/install-server.sh"
        REPOSITORY_REF="v${legacy}"
        reject_legacy_release_before_write
    ); then
        fail_test "source installer accepted legacy release $legacy"
    fi
done

for legacy in 1.2.9 1.5.7 1.5.7.1; do
    updater_directory="$(mktemp -d)"
    printf 'CANVAS_IMAGE_TAG=%s\n' "$legacy" >"$updater_directory/.env"
    if (
        source_without_main "$ROOT/scripts/install-host-updater.sh"
        INSTALL_DIR="$updater_directory"
        read_image_tag
    ); then
        rm -rf "$updater_directory"
        fail_test "host updater accepted legacy release $legacy"
    fi
    [[ "$(find "$updater_directory" -mindepth 1 -maxdepth 1 -printf '%f\n')" == ".env" ]] ||
        fail_test "host updater wrote before rejecting legacy release $legacy"
    rm -rf "$updater_directory"
done

printf 'deployment script contract tests passed\n'
