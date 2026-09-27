#!/usr/bin/env bash

set -Eeuo pipefail

HOST_UPDATER_CONTRACT_VERSION=2
INSTALL_DIR="${INSTALL_DIR:-/opt/open-ai-canvas}"
REPOSITORY="${REPOSITORY:-ddcat-ai/open-ai-canvas}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-}"
REQUESTED_SOCKET_DIR="${CANVAS_UPDATER_SOCKET_DIR:-}"
SOCKET_DIR=""
UPDATER_BIN="/usr/local/bin/open-ai-canvas-host-updater"
UPDATER_ENV="/etc/open-ai-canvas-updater.env"
UPDATER_SERVICE="/etc/systemd/system/open-ai-canvas-updater.service"

fail() {
    printf 'Host Updater 安装失败：%s\n' "$1" >&2
    exit 1
}

validate_image_repository() {
    local repository="$1"
    [[ "$repository" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*(:[0-9]+)?(/[A-Za-z0-9][A-Za-z0-9._-]*)+$ ]] ||
        fail "IMAGE_REPOSITORY 必须是安全的 registry/repository 路径"
    [[ "$repository" != *".."* &&
        "$repository" != *//* &&
        "$repository" != *\\* ]] ||
        fail "IMAGE_REPOSITORY 含有不安全路径片段"
}

image_repository() {
    if [[ -n "$IMAGE_REPOSITORY" ]]; then
        validate_image_repository "$IMAGE_REPOSITORY"
        printf '%s' "$IMAGE_REPOSITORY"
        return
    fi
    validate_image_repository "$REPOSITORY"
    printf 'ghcr.io/%s' "$REPOSITORY"
}

normalize_compose_image_contract() {
    local compose_path="$1"
    local output_path="$2"
    local repository
    repository="$(image_repository)"
    awk -v backend_legacy="${repository}-backend:\${CANVAS_IMAGE_TAG:-latest}" \
        -v web_legacy="${repository}-web:\${CANVAS_IMAGE_TAG:-latest}" '
        function image_value(line,    value) {
            value = line
            sub(/^[ \t]*image:[ \t]*/, "", value)
            sub(/^"/, "", value)
            sub(/"[ \t]*(#.*)?$/, "", value)
            sub(/[ \t]+#.*/, "", value)
            return value
        }
        function uses_variable(value, variable,    prefix) {
            if (value == "${" variable "}") return 1
            prefix = "${" variable ":?"
            return index(value, prefix) == 1 && substr(value, length(value), 1) == "}" && length(value) > length(prefix) + 1
        }
        {
            line = $0
            trimmed = line
            sub(/^[ \t]*/, "", trimmed)
            sub(/[ \t]*$/, "", trimmed)
            current_indent = length(line) - length(trimmed)
            if (trimmed == "" || trimmed ~ /^#/) {
                print line
                next
            }
            if (!in_services) {
                print line
                if (trimmed == "services:") {
                    in_services = 1
                    services_indent = current_indent
                }
                next
            }
            if (current_indent <= services_indent) {
                in_services = 0
                service = ""
                print line
                next
            }
            if (service != "" && current_indent <= service_indent) service = ""
            if (service == "" && current_indent == services_indent + 2 && trimmed ~ /^[^:]+:[ \t]*(#.*)?$/) {
                service = trimmed
                sub(/[ \t]*#.*/, "", service)
                sub(/:.*/, "", service)
                service_indent = current_indent
                print line
                next
            }
            if (service != "" && current_indent == service_indent + 2 && trimmed ~ /^image:[ \t]*/) {
                value = image_value(line)
                variable = (service == "web" ? "CANVAS_WEB_IMAGE" : "CANVAS_BACKEND_IMAGE")
                legacy = (service == "web" ? web_legacy : backend_legacy)
                if (service == "backend" || service == "migrate" || service == "web") {
                    if (uses_variable(value, variable)) {
                        found[service]++
                        print line
                        next
                    }
                    if (value == legacy) {
                        found[service]++
                        prefix = line
                        sub(/image:.*/, "", prefix)
                        print prefix "image: ${" variable ":?请先配置 " variable "}"
                        next
                    }
                    bad = service
                }
            }
            print line
        }
        END {
            if (!found["backend"] || !found["migrate"] || !found["web"] || bad != "") exit 1
        }
    ' "$compose_path" >"$output_path" ||
        fail "部署 Compose 不是安全变量格式，且不是受支持的官方 legacy 格式"
}

normalize_compose_file() {
    local path="${INSTALL_DIR}/docker-compose.deploy.yml"
    local temporary
    temporary="$(mktemp "${path}.XXXXXX")"
    normalize_compose_image_contract "$path" "$temporary"
    chmod --reference="$path" "$temporary"
    mv "$temporary" "$path"
}

set_env_values() {
    local path="$1"
    shift
    (( $# > 0 && $# % 2 == 0 )) || fail "环境变量更新参数无效"
    local temporary updates
    temporary="$(mktemp "${path}.XXXXXX")"
    updates="$(mktemp "${path}.updates.XXXXXX")"
    umask 077
    while (( $# > 0 )); do
        printf '%s=%s\n' "$1" "$2" >>"$updates"
        shift 2
    done
    awk '
        NR == FNR {
            key = $0
            sub(/=.*/, "", key)
            value = $0
            sub(/^[^=]*=/, "", value)
            if (!(key in update)) order[++order_count] = key
            update[key] = value
            next
        }
        {
            key = $0
            sub(/^[ \t]*/, "", key)
            sub(/=.*/, "", key)
            if (key in update) {
                if (!(key in written)) printf "%s\n", key "=" update[key]
                written[key] = 1
                next
            }
            printf "%s\n", $0
        }
        END {
            for (order_index = 1; order_index <= order_count; order_index++) {
                key = order[order_index]
                if (!(key in written)) printf "%s\n", key "=" update[key]
            }
        }
    ' "$updates" "$path" >"$temporary"
    rm -f "$updates"
    chmod --reference="$path" "$temporary"
    mv "$temporary" "$path"
}

validate_digest() {
    local value="$1"
    local component="$2"
    local repository
    repository="$(image_repository)"
    local prefix="${repository}-${component}@sha256:"
    [[ "$value" == "$prefix"* ]] || return 1
    [[ "${value#"$prefix"}" =~ ^[a-f0-9]{64}$ ]]
}

resolve_local_digest() {
    local reference="$1"
    local repository="$2"
    docker image inspect "$reference" --format '{{range .RepoDigests}}{{println .}}{{end}}' |
        awk -v repository="$repository" 'index($0, repository "@sha256:") == 1 && $0 ~ /@sha256:[a-f0-9]{64}$/ { print; exit }'
}

prepare_deployment_contract() {
    local tag backend web repository
    repository="$(image_repository)"
    tag="$(sed -n 's/^CANVAS_IMAGE_TAG=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    [[ -n "$tag" && "$tag" != "latest" ]] || fail "CANVAS_IMAGE_TAG 必须固定为具体 Release"
    backend="$(sed -n 's/^CANVAS_BACKEND_IMAGE=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    web="$(sed -n 's/^CANVAS_WEB_IMAGE=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    local resolved_backend="$backend"
    local resolved_web="$web"
    if ! validate_digest "$backend" backend; then
        resolved_backend="$(resolve_local_digest "${repository}-backend:${tag}" "${repository}-backend")"
        validate_digest "$resolved_backend" backend ||
            fail "现有部署的 backend 镜像不是 digest，且无法从本地镜像安全解析；未改写 Compose/.env，也未执行 pull"
    fi
    if ! validate_digest "$web" web; then
        resolved_web="$(resolve_local_digest "${repository}-web:${tag}" "${repository}-web")"
        validate_digest "$resolved_web" web ||
            fail "现有部署的 web 镜像不是 digest，且无法从本地镜像安全解析；未改写 Compose/.env，也未执行 pull"
    fi
    normalize_compose_file
    if [[ "$resolved_backend" != "$backend" || "$resolved_web" != "$web" ]]; then
        set_env_values "${INSTALL_DIR}/.env" \
            CANVAS_BACKEND_IMAGE "$resolved_backend" \
            CANVAS_WEB_IMAGE "$resolved_web"
    fi
    validate_digest "$resolved_backend" backend || fail "CANVAS_BACKEND_IMAGE 必须固定为匹配 GHCR 的 digest"
    validate_digest "$resolved_web" web || fail "CANVAS_WEB_IMAGE 必须固定为匹配 GHCR 的 digest"
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
    case "${configured#v}" in
        1.2.9|1.5.7|1.5.7.1)
            fail "Release v${configured#v} 的 Host Updater 是 tag-based 旧二进制，已阻止静默安装；请使用带 digest contract 的 updater Release，或仅运行镜像安装器的 CANVAS_SKIP_HOST_UPDATER=1 模式"
            ;;
    esac
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
    local token
    token="$(sed -n 's/^CANVAS_UPDATER_TOKEN=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    if [[ -z "$token" ]]; then
        token="$(openssl rand -hex 32)"
        set_env_values "${INSTALL_DIR}/.env" CANVAS_UPDATER_TOKEN "$token"
    fi
    [[ ${#token} -ge 32 ]] || fail "CANVAS_UPDATER_TOKEN 长度不足"
    umask 077
    if [[ -n "$IMAGE_REPOSITORY" ]]; then
        printf 'CANVAS_UPDATER_TOKEN=%s\nCANVAS_UPDATER_REPOSITORY=%s\nCANVAS_UPDATER_IMAGE_REPOSITORY=%s\nCANVAS_UPDATER_INSTALL_DIR=%s\nCANVAS_UPDATER_SOCKET=%s/updater.sock\n' \
            "$token" "$REPOSITORY" "$IMAGE_REPOSITORY" "$INSTALL_DIR" "$SOCKET_DIR" >"$UPDATER_ENV"
    else
        printf 'CANVAS_UPDATER_TOKEN=%s\nCANVAS_UPDATER_REPOSITORY=%s\nCANVAS_UPDATER_INSTALL_DIR=%s\nCANVAS_UPDATER_SOCKET=%s/updater.sock\n' \
            "$token" "$REPOSITORY" "$INSTALL_DIR" "$SOCKET_DIR" >"$UPDATER_ENV"
    fi
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
    read_image_tag
    prepare_deployment_contract
    resolve_socket_dir
    install_binary
    ensure_token
    install_service
    printf 'Host Updater 已安装，Socket：%s/updater.sock\n' "$SOCKET_DIR"
    printf '请重建 backend 容器，使 Token 与 Socket 挂载生效。\n'
}

main "$@"
