#!/usr/bin/env bash

set -Eeuo pipefail

INSTALL_DIR="${INSTALL_DIR:-/opt/open-ai-canvas}"
REPOSITORY="${REPOSITORY:-ddcat-ai/open-ai-canvas}"
REQUESTED_SOCKET_DIR="${CANVAS_UPDATER_SOCKET_DIR:-}"
SOCKET_DIR=""
UPDATER_BIN="/usr/local/bin/open-ai-canvas-host-updater"
UPDATER_ENV="/etc/open-ai-canvas-updater.env"
UPDATER_SERVICE="/etc/systemd/system/open-ai-canvas-updater.service"

fail() {
    printf 'Host Updater 安装失败：%s\n' "$1" >&2
    exit 1
}

is_expected_digest_image() {
    local value="$1"
    local repository="$2"
    local digest
    [[ "$value" == "${repository}@sha256:"* ]] || return 1
    digest="${value#${repository}@sha256:}"
    [[ "$digest" =~ ^[a-f0-9]{64}$ ]]
}

validate_compose_image_contract() {
    local compose_path="$1"
    awk '
        function indent(line,    prefix) {
            prefix = line
            sub(/[^ \t].*/, "", prefix)
            return length(prefix)
        }
        function uses_image_variable(line, variable,    value, prefix, next_char) {
            value = line
            sub(/^[ \t]*image:[ \t]*/, "", value)
            sub(/^["\047]/, "", value)
            prefix = "${" variable
            if (index(value, prefix) != 1) {
                return 0
            }
            next_char = substr(value, length(prefix) + 1, 1)
            return next_char == "}" || next_char == ":" || next_char == "?" || next_char == "-" || next_char == "+"
        }
        {
            current_indent = indent($0)
            trimmed = $0
            sub(/^[ \t]*/, "", trimmed)
            sub(/[ \t]*$/, "", trimmed)
            if (trimmed == "" || trimmed ~ /^#/) {
                next
            }
            if (!in_services) {
                if (trimmed == "services:") {
                    in_services = 1
                    services_indent = current_indent
                }
                next
            }
            if (current_indent <= services_indent) {
                in_services = 0
                service = ""
                next
            }
            if (service != "" && current_indent <= service_indent) {
                service = ""
            }
            if (service == "" && current_indent == services_indent + 2 && trimmed ~ /^[^:]+:[ \t]*(#.*)?$/) {
                service = trimmed
                sub(/[ \t]*#.*/, "", service)
                sub(/:.*/, "", service)
                service_indent = current_indent
                next
            }
            if (service != "" && current_indent == service_indent + 2 && trimmed ~ /^image:[ \t]*/) {
                if (service == "backend" && uses_image_variable($0, "CANVAS_BACKEND_IMAGE")) {
                    found_backend = 1
                }
                if (service == "migrate" && uses_image_variable($0, "CANVAS_BACKEND_IMAGE")) {
                    found_migrate = 1
                }
                if (service == "web" && uses_image_variable($0, "CANVAS_WEB_IMAGE")) {
                    found_web = 1
                }
            }
        }
        END {
            if (!found_backend || !found_migrate || !found_web) {
                exit 1
            }
        }
    ' "$compose_path" ||
        fail "部署 Compose 的 services.backend、services.migrate、services.web 必须分别使用对应镜像变量，已拒绝旧格式"
}

require_root() {
    [[ "${EUID}" -eq 0 ]] || fail "请使用 sudo 运行"
    [[ "$(uname -s)" == "Linux" ]] || fail "仅支持 Linux 服务器"
    command -v systemctl >/dev/null 2>&1 || fail "服务器必须使用 systemd"
    command -v curl >/dev/null 2>&1 || fail "缺少 curl"
    command -v sha256sum >/dev/null 2>&1 || fail "缺少 sha256sum"
    command -v openssl >/dev/null 2>&1 || fail "缺少 openssl"
    [[ -f "${INSTALL_DIR}/.env" ]] || fail "未找到 ${INSTALL_DIR}/.env"
    [[ -f "${INSTALL_DIR}/docker-compose.deploy.yml" ]] || fail "未找到部署 Compose"
    validate_compose_image_contract "${INSTALL_DIR}/docker-compose.deploy.yml"
    local backend_image web_image
    backend_image="$(sed -n 's/^CANVAS_BACKEND_IMAGE=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    web_image="$(sed -n 's/^CANVAS_WEB_IMAGE=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    local repository_owner="${REPOSITORY%%/*}"
    local expected_image_repository="ghcr.io/${repository_owner}/open-ai-canvas"
    is_expected_digest_image "$backend_image" "${expected_image_repository}-backend" || fail "CANVAS_BACKEND_IMAGE 必须固定为 ${expected_image_repository}-backend 的 GHCR digest"
    is_expected_digest_image "$web_image" "${expected_image_repository}-web" || fail "CANVAS_WEB_IMAGE 必须固定为 ${expected_image_repository}-web 的 GHCR digest"
}

set_env_value() {
    local path="$1"
    local key="$2"
    local value="$3"
    local temporary
    temporary="$(mktemp "${path}.XXXXXX")"
    awk -v key="$key" -v value="$value" '
        BEGIN { updated=0 }
        $0 ~ "^" key "=" { print key "=" value; updated=1; next }
        { print }
        END { if (!updated) print key "=" value }
    ' "$path" > "$temporary"
    chmod --reference="$path" "$temporary"
    mv "$temporary" "$path"
}

resolve_socket_dir() {
    local configured_socket_dir
    configured_socket_dir="$(sed -n 's/^CANVAS_UPDATER_SOCKET_DIR=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    if [[ -n "$REQUESTED_SOCKET_DIR" ]]; then
        SOCKET_DIR="$REQUESTED_SOCKET_DIR"
        set_env_value "${INSTALL_DIR}/.env" CANVAS_UPDATER_SOCKET_DIR "$SOCKET_DIR"
    elif [[ -n "$configured_socket_dir" ]]; then
        SOCKET_DIR="$configured_socket_dir"
    else
        SOCKET_DIR="/run/open-ai-canvas-updater"
    fi
    [[ "$SOCKET_DIR" == /* ]] || fail "CANVAS_UPDATER_SOCKET_DIR 必须是绝对路径"
}

read_image_tag() {
    local configured
    configured="$(sed -n 's/^CANVAS_IMAGE_TAG=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    [[ -n "$configured" && "$configured" != "latest" ]] || fail "请先把 CANVAS_IMAGE_TAG 固定为已发布版本"
    if [[ "$configured" == v* ]]; then
        RELEASE_TAG="$configured"
    else
        RELEASE_TAG="v${configured}"
    fi
}

install_binary() {
    local arch asset temporary checksum_file expected
    case "$(uname -m)" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) fail "不支持的 CPU 架构：$(uname -m)" ;;
    esac
    asset="open-ai-canvas-host-updater-linux-${arch}"
    temporary="$(mktemp)"
    checksum_file="$(mktemp)"
    curl -fsSL "https://github.com/${REPOSITORY}/releases/download/${RELEASE_TAG}/${asset}" -o "$temporary"
    curl -fsSL "https://github.com/${REPOSITORY}/releases/download/${RELEASE_TAG}/SHA256SUMS" -o "$checksum_file"
    expected="$(awk -v asset="$asset" '$2 == asset { print $1 }' "$checksum_file")"
    [[ "$expected" =~ ^[a-f0-9]{64}$ ]] || fail "Release 校验清单缺少 ${asset}"
    printf '%s  %s\n' "$expected" "$temporary" | sha256sum -c - >/dev/null || fail "Host Updater SHA-256 校验失败"
    install -m 0755 "$temporary" "$UPDATER_BIN"
    rm -f "$temporary" "$checksum_file"
}

ensure_token() {
    local token temporary
    token="$(sed -n 's/^CANVAS_UPDATER_TOKEN=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    if [[ -z "$token" ]]; then
        token="$(openssl rand -hex 32)"
        temporary="$(mktemp "${INSTALL_DIR}/.env.XXXXXX")"
        awk -v token="$token" '
            BEGIN { updated=0 }
            /^CANVAS_UPDATER_TOKEN=/ { print "CANVAS_UPDATER_TOKEN=" token; updated=1; next }
            { print }
            END { if (!updated) print "CANVAS_UPDATER_TOKEN=" token }
        ' "${INSTALL_DIR}/.env" > "$temporary"
        chmod --reference="${INSTALL_DIR}/.env" "$temporary"
        mv "$temporary" "${INSTALL_DIR}/.env"
    fi
    [[ ${#token} -ge 32 ]] || fail "CANVAS_UPDATER_TOKEN 长度不足"
    umask 077
    printf 'CANVAS_UPDATER_TOKEN=%s\nCANVAS_UPDATER_INSTALL_DIR=%s\nCANVAS_UPDATER_SOCKET=%s/updater.sock\n' "$token" "$INSTALL_DIR" "$SOCKET_DIR" > "$UPDATER_ENV"
}

install_service() {
    local temporary_service
    install -d -m 0755 "$SOCKET_DIR"
    install -d -m 0700 /var/lib/open-ai-canvas-updater "${INSTALL_DIR}/backups"
    temporary_service="$(mktemp)"
    printf '%s\n' \
        '[Unit]' \
        'Description=Open AI Canvas Host Updater' \
        'After=docker.service network-online.target' \
        'Requires=docker.service' \
        'Wants=network-online.target' \
        '' \
        '[Service]' \
        'Type=simple' \
        "EnvironmentFile=${UPDATER_ENV}" \
        "ExecStart=${UPDATER_BIN}" \
        'Restart=on-failure' \
        'RestartSec=5s' \
        'NoNewPrivileges=true' \
        'PrivateTmp=true' \
        'ProtectHome=true' \
        'ProtectSystem=full' \
        "ReadWritePaths=${INSTALL_DIR} /var/lib/open-ai-canvas-updater ${SOCKET_DIR} /usr/local/bin" \
        '' \
        '[Install]' \
        'WantedBy=multi-user.target' > "$temporary_service"
    install -m 0644 "$temporary_service" "$UPDATER_SERVICE"
    rm -f "$temporary_service"
    systemctl daemon-reload
    systemctl enable --now open-ai-canvas-updater.service
    systemctl restart open-ai-canvas-updater.service
}

main() {
    require_root
    resolve_socket_dir
    read_image_tag
    install_binary
    ensure_token
    install_service
    printf 'Host Updater 已安装，Socket：%s/updater.sock\n' "$SOCKET_DIR"
    printf '请重建 backend 容器，使 Token 与 Socket 挂载生效。\n'
}

main "$@"
