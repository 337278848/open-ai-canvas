#!/usr/bin/env bash

set -Eeuo pipefail

REPOSITORY_REF="${REPOSITORY_REF:-main}"
REPOSITORY="${REPOSITORY:-337278848/open-ai-canvas}"
INSTALL_DIR="${INSTALL_DIR:-/opt/open-ai-canvas}"
CANVAS_HTTP_PORT="${CANVAS_HTTP_PORT:-3000}"
REQUESTED_IMAGE_TAG="${CANVAS_IMAGE_TAG:-}"
CANVAS_IMAGE_TAG="${REQUESTED_IMAGE_TAG#v}"
DEFAULT_IMAGE_REPOSITORY="ghcr.io/${REPOSITORY}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-$DEFAULT_IMAGE_REPOSITORY}"
CANVAS_BACKEND_IMAGE=""
CANVAS_WEB_IMAGE=""
REQUESTED_UPDATER_SOCKET_DIR="${CANVAS_UPDATER_SOCKET_DIR:-}"
COMPOSE_FILE="docker-compose.deploy.yml"
COMPOSE_REF="${COMPOSE_REF:-}"
COMPOSE_URL="${COMPOSE_URL:-}"
RELEASE_REF="${RELEASE_REF:-}"
UPDATER_INSTALL_URL="${UPDATER_INSTALL_URL:-}"
UPDATER_REPOSITORY="${UPDATER_REPOSITORY:-$REPOSITORY}"
LEGACY_RELEASE_SKIP=0

step() {
    printf '\n==> %s\n' "$1"
}

fail() {
    printf '\n安装失败：%s\n' "$1" >&2
    exit 1
}

is_legacy_release_ref() {
    local ref="$1"
    ref="${ref#refs/tags/}"
    ref="${ref#v}"
    case "$ref" in
        1.2.9|1.5.7|1.5.7.1) return 0 ;;
        *) return 1 ;;
    esac
}

load_configured_image_tag() {
    [[ -n "$CANVAS_IMAGE_TAG" || ! -f "${INSTALL_DIR}/.env" ]] && return
    local configured
    configured="$(sed -n 's/^CANVAS_IMAGE_TAG=//p' "${INSTALL_DIR}/.env" | tail -n 1)"
    [[ -n "$configured" ]] || return
    CANVAS_IMAGE_TAG="${configured#v}"
}

validate_updater_repository() {
    [[ "$UPDATER_REPOSITORY" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$ ]] ||
        fail "UPDATER_REPOSITORY 必须是安全的 owner/repository"
    [[ "$UPDATER_REPOSITORY" != *".."* && "$UPDATER_REPOSITORY" != *//* && "$UPDATER_REPOSITORY" != *\\* ]] ||
        fail "UPDATER_REPOSITORY 含有不安全路径片段"
}

resolve_release_ref() {
    if [[ -n "$COMPOSE_REF" && -n "$RELEASE_REF" && "$COMPOSE_REF" != "$RELEASE_REF" ]]; then
        fail "COMPOSE_REF 与 RELEASE_REF 不一致；Compose 和 Host Updater 必须使用同一个 release ref"
    fi
    local tag_ref=""
    local explicit_ref="$RELEASE_REF"
    [[ -n "$explicit_ref" ]] || explicit_ref="$COMPOSE_REF"
    if [[ -n "$CANVAS_IMAGE_TAG" ]]; then
        tag_ref="v${CANVAS_IMAGE_TAG}"
    fi
    if [[ -n "$explicit_ref" &&
        -n "$tag_ref" &&
        "$explicit_ref" != "$tag_ref" &&
        ( -z "$RELEASE_REF" || -z "$COMPOSE_REF" ) ]]; then
        fail "CANVAS_IMAGE_TAG、Compose 和 Host Updater 必须指向同一个 release ref（期望 ${tag_ref}）"
    fi
    if [[ -n "$explicit_ref" ]]; then
        RELEASE_REF="$explicit_ref"
    elif [[ -n "$tag_ref" ]]; then
        RELEASE_REF="$tag_ref"
    else
        RELEASE_REF="$REPOSITORY_REF"
    fi
    [[ "$RELEASE_REF" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] ||
        fail "RELEASE_REF 不是有效的 Git ref"
    [[ "$RELEASE_REF" != *".."* &&
        "$RELEASE_REF" != /* &&
        "$RELEASE_REF" != */ &&
        "$RELEASE_REF" != *//* &&
        "$RELEASE_REF" != *\\* ]] ||
        fail "RELEASE_REF 含有不安全路径片段"
    COMPOSE_REF="$RELEASE_REF"
    validate_updater_repository
    [[ -n "$COMPOSE_URL" ]] || COMPOSE_URL="https://raw.githubusercontent.com/${REPOSITORY}/${RELEASE_REF}/${COMPOSE_FILE}"
    [[ -n "$UPDATER_INSTALL_URL" ]] || UPDATER_INSTALL_URL="https://raw.githubusercontent.com/${UPDATER_REPOSITORY}/${RELEASE_REF}/scripts/install-host-updater.sh"
}

guard_legacy_release_before_write() {
    resolve_release_ref
    if ! is_legacy_release_ref "$RELEASE_REF" && ! is_legacy_release_ref "v${CANVAS_IMAGE_TAG}"; then
        return
    fi
    if [[ "${CANVAS_SKIP_HOST_UPDATER:-0}" != "1" ]]; then
        fail "Release ${RELEASE_REF} 使用 tag-based legacy Compose/Host Updater；为避免先写入部署文件，已拒绝安装。若确认只部署固定 digest 镜像，请显式设置 CANVAS_SKIP_HOST_UPDATER=1"
    fi
    LEGACY_RELEASE_SKIP=1
    printf '\n警告：已在任何部署文件写入前按显式 CANVAS_SKIP_HOST_UPDATER=1 跳过 Release %s 的旧 Host Updater；当前部署仍必须固定镜像 digest。\n' "$RELEASE_REF"
}

normalize_compose_image_contract() {
    local compose_path="$1"
    local output_path="$2"
    awk -v backend_legacy="${IMAGE_REPOSITORY}-backend:\${CANVAS_IMAGE_TAG:-latest}" \
        -v web_legacy="${IMAGE_REPOSITORY}-web:\${CANVAS_IMAGE_TAG:-latest}" '
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
                        converted = 1
                        next
                    }
                    bad = service
                }
            }
            print line
        }
        END {
            for (name in found) if (found[name] != 1) bad = name
            if (!found["backend"] || !found["migrate"] || !found["web"] || bad != "") exit 1
        }
    ' "$compose_path" >"$output_path" ||
        fail "部署 Compose 的 services.backend、services.migrate、services.web 必须使用安全镜像变量或受支持的官方 legacy 格式"
}

validate_compose_image_contract() {
    local compose_path="$1"
    local temporary
    temporary="$(mktemp)"
    normalize_compose_image_contract "$compose_path" "$temporary"
    rm -f "$temporary"
}

normalize_compose_file() {
    local compose_path="$1"
    local temporary
    temporary="$(mktemp "${compose_path}.XXXXXX")"
    normalize_compose_image_contract "$compose_path" "$temporary"
    chmod --reference="$compose_path" "$temporary"
    mv "$temporary" "$compose_path"
}

validate_image_repository() {
    [[ "$IMAGE_REPOSITORY" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*(:[0-9]+)?(/[A-Za-z0-9][A-Za-z0-9._-]*)+$ ]] ||
        fail "IMAGE_REPOSITORY 必须是安全的 registry/repository 路径"
    [[ "$IMAGE_REPOSITORY" != *".."* &&
        "$IMAGE_REPOSITORY" != *//* &&
        "$IMAGE_REPOSITORY" != *\\* ]] ||
        fail "IMAGE_REPOSITORY 含有不安全路径片段"
}

validate_image_digest() {
    local value="$1"
    local component="$2"
    local prefix="${IMAGE_REPOSITORY}-${component}@sha256:"
    [[ "$value" == "$prefix"* ]] || return 1
    local digest="${value#"$prefix"}"
    [[ "$digest" =~ ^[a-f0-9]{64}$ ]]
}

validate_deployment_digests() {
    local backend_image web_image
    backend_image="$(sed -n 's/^CANVAS_BACKEND_IMAGE=//p' .env | tail -n 1)"
    web_image="$(sed -n 's/^CANVAS_WEB_IMAGE=//p' .env | tail -n 1)"
    validate_image_digest "$backend_image" backend ||
        fail "CANVAS_BACKEND_IMAGE 必须固定为 ${IMAGE_REPOSITORY}-backend@sha256:<64位十六进制摘要>"
    validate_image_digest "$web_image" web ||
        fail "CANVAS_WEB_IMAGE 必须固定为 ${IMAGE_REPOSITORY}-web@sha256:<64位十六进制摘要>"
}

require_root() {
    validate_image_repository
    if [[ "${EUID}" -ne 0 ]]; then
        fail "请使用 README 中带 sudo 的一键安装命令"
    fi
    [[ "$(uname -s)" == "Linux" ]] || fail "一键部署脚本仅支持 Linux 服务器"
    [[ "$CANVAS_HTTP_PORT" =~ ^[0-9]+$ ]] || fail "CANVAS_HTTP_PORT 必须是 1 到 65535 的数字"
    ((CANVAS_HTTP_PORT >= 1 && CANVAS_HTTP_PORT <= 65535)) || fail "CANVAS_HTTP_PORT 必须是 1 到 65535 的数字"
    if [[ -n "$CANVAS_IMAGE_TAG" ]]; then
        [[ "$CANVAS_IMAGE_TAG" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]] || fail "CANVAS_IMAGE_TAG 不是有效的 Docker 镜像标签"
        [[ "$CANVAS_IMAGE_TAG" != "latest" ]] || fail "生产部署必须固定到具体 Release，不能使用 latest"
    fi
}

install_packages() {
    local packages=(ca-certificates curl openssl)

    if command -v apt-get >/dev/null 2>&1; then
        apt-get update
        DEBIAN_FRONTEND=noninteractive apt-get install -y "${packages[@]}"
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y "${packages[@]}"
    elif command -v yum >/dev/null 2>&1; then
        yum install -y "${packages[@]}"
    else
        fail "暂不支持当前 Linux 发行版，请先手动安装 Docker、curl 和 OpenSSL"
    fi
}

install_docker() {
    if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
        return
    fi

    step "安装 Docker 和 Docker Compose"
    local installer
    installer="$(mktemp)"
    curl -fsSL https://get.docker.com -o "$installer"
    sh "$installer"
    rm -f "$installer"

    if command -v systemctl >/dev/null 2>&1; then
        systemctl enable --now docker
    elif command -v service >/dev/null 2>&1; then
        service docker start
    fi

    docker compose version >/dev/null 2>&1 || fail "Docker Compose 安装失败"
}

login_ghcr() {
    if [[ -n "${GHCR_USERNAME:-}" || -n "${GHCR_TOKEN:-}" ]]; then
        [[ -n "${GHCR_USERNAME:-}" && -n "${GHCR_TOKEN:-}" ]] || fail "GHCR_USERNAME 和 GHCR_TOKEN 必须同时配置"
        step "登录 GitHub Container Registry"
        # token 只通过 stdin 交给 Docker，避免出现在命令参数和进程列表中。
        printf '%s' "$GHCR_TOKEN" | docker login ghcr.io --username "$GHCR_USERNAME" --password-stdin
    fi
}

prepare_environment() {
    mkdir -p "$INSTALL_DIR"
    cd "$INSTALL_DIR"

    if [[ -f .env ]]; then
        grep -Eq '^POSTGRES_PASSWORD=.+$' .env || fail "现有 .env 缺少 POSTGRES_PASSWORD"
        grep -Eq '^DATABASE_URL=.+$' .env || fail "现有 .env 缺少 DATABASE_URL"

        local configured_http_port
        configured_http_port="$(sed -n 's/^CANVAS_HTTP_PORT=//p' .env | tail -n 1)"
        if [[ -n "$configured_http_port" ]]; then
            [[ "$configured_http_port" =~ ^[0-9]+$ ]] || fail ".env 中的 CANVAS_HTTP_PORT 无效"
            ((configured_http_port >= 1 && configured_http_port <= 65535)) || fail ".env 中的 CANVAS_HTTP_PORT 无效"
            CANVAS_HTTP_PORT="$configured_http_port"
        fi

        local configured_image_tag
        configured_image_tag="$(sed -n 's/^CANVAS_IMAGE_TAG=//p' .env | tail -n 1)"
        if [[ -n "$REQUESTED_IMAGE_TAG" ]]; then
            set_env_values .env CANVAS_IMAGE_TAG "$CANVAS_IMAGE_TAG"
        elif [[ -n "$configured_image_tag" ]]; then
            [[ "$configured_image_tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ && "$configured_image_tag" != "latest" ]] || fail ".env 中的 CANVAS_IMAGE_TAG 必须固定为具体 Release"
            CANVAS_IMAGE_TAG="${configured_image_tag#v}"
        else
            fail "请通过 CANVAS_IMAGE_TAG 指定具体 Release，例如 v1.5.7.1"
        fi
        resolve_updater_socket_dir
        return
    fi

    [[ -n "$CANVAS_IMAGE_TAG" ]] || fail "首次安装必须通过 CANVAS_IMAGE_TAG 指定具体 Release，例如 v1.5.7.1"
    [[ "$CANVAS_IMAGE_TAG" != "latest" ]] || fail "生产部署必须固定到具体 Release，不能使用 latest"

    step "生成 PostgreSQL 随机密码和部署配置"
    local database_password
    database_password="$(openssl rand -hex 32)"
    umask 077
    local temporary_env
    temporary_env="$(mktemp "${INSTALL_DIR}/.env.XXXXXX")"
    chmod 600 "$temporary_env"
    cat >"$temporary_env" <<EOF
POSTGRES_DB=open_ai_canvas
POSTGRES_USER=open_ai_canvas
POSTGRES_PASSWORD=${database_password}
DATABASE_URL=postgresql://open_ai_canvas:${database_password}@postgres:5432/open_ai_canvas?sslmode=disable
CANVAS_HTTP_PORT=${CANVAS_HTTP_PORT}
CANVAS_IMAGE_TAG=${CANVAS_IMAGE_TAG}
CANVAS_REGISTRATION_ENABLED=false
CANVAS_ALLOW_PRIVATE_UPSTREAMS=false
CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS=
CANVAS_CORS_ORIGINS=
EOF
    mv "$temporary_env" .env
    resolve_updater_socket_dir
}

set_env_value() {
    local path="$1"
    local key="$2"
    local value="$3"
    set_env_values "$path" "$key" "$value"
}

set_env_values() {
    local path="$1"
    shift
    (( $# > 0 && $# % 2 == 0 )) || fail "环境变量更新参数无效"
    local temporary
    local updates
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

resolve_updater_socket_dir() {
    local configured_socket_dir
    configured_socket_dir="$(sed -n 's/^CANVAS_UPDATER_SOCKET_DIR=//p' .env | tail -n 1)"
    if [[ -n "$REQUESTED_UPDATER_SOCKET_DIR" ]]; then
        CANVAS_UPDATER_SOCKET_DIR="$REQUESTED_UPDATER_SOCKET_DIR"
        set_env_value .env CANVAS_UPDATER_SOCKET_DIR "$CANVAS_UPDATER_SOCKET_DIR"
    elif [[ -n "$configured_socket_dir" ]]; then
        CANVAS_UPDATER_SOCKET_DIR="$configured_socket_dir"
    else
        CANVAS_UPDATER_SOCKET_DIR="/run/open-ai-canvas-updater"
    fi
    [[ "$CANVAS_UPDATER_SOCKET_DIR" == /* ]] || fail "CANVAS_UPDATER_SOCKET_DIR 必须是绝对路径"
}

resolve_compose_url() {
    resolve_release_ref
}

download_compose() {
    resolve_compose_url
    step "下载并规范化 release ref=${RELEASE_REF} 的 GHCR 部署配置"
    local temporary_file
    temporary_file="$(mktemp "${INSTALL_DIR}/.docker-compose.deploy.XXXXXX")"
    curl -fsSL "$COMPOSE_URL" -o "$temporary_file"
    local normalized_file
    normalized_file="$(mktemp "${INSTALL_DIR}/.docker-compose.deploy.normalized.XXXXXX")"
    normalize_compose_image_contract "$temporary_file" "$normalized_file"
    chmod --reference="$temporary_file" "$normalized_file"
    rm -f "$temporary_file"
    temporary_file="$normalized_file"
    mv "$temporary_file" "$COMPOSE_FILE"
}

install_host_updater() {
    (( LEGACY_RELEASE_SKIP == 1 )) && return
    step "安装宿主机在线更新服务"
    local installer
    installer="$(mktemp)"
    curl -fsSL "$UPDATER_INSTALL_URL" -o "$installer"
    grep -Fqx 'HOST_UPDATER_CONTRACT_VERSION=2' "$installer" ||
        fail "release ref=${RELEASE_REF} 的 Host Updater 安装脚本不支持 digest contract，已阻止静默安装；请使用当前分支脚本或显式跳过更新器"
    INSTALL_DIR="$INSTALL_DIR" REPOSITORY="$UPDATER_REPOSITORY" IMAGE_REPOSITORY="$IMAGE_REPOSITORY" \
        CANVAS_UPDATER_SOCKET_DIR="$CANVAS_UPDATER_SOCKET_DIR" bash "$installer"
    rm -f "$installer"
}

pull_and_pin_images() {
    step "拉取并固定 GHCR 网页与后端镜像 digest"
    CANVAS_IMAGE_TAG="$CANVAS_IMAGE_TAG" \
        CANVAS_BACKEND_IMAGE="${IMAGE_REPOSITORY}-backend:${CANVAS_IMAGE_TAG}" \
        CANVAS_WEB_IMAGE="${IMAGE_REPOSITORY}-web:${CANVAS_IMAGE_TAG}" \
        docker compose --env-file .env -f "$COMPOSE_FILE" pull backend web migrate || {
        fail "GHCR 镜像拉取失败；如果容器包尚未公开，请通过 GHCR_USERNAME 和 GHCR_TOKEN 登录后重试"
    }
    local backend_digest web_digest
    CANVAS_BACKEND_IMAGE="${IMAGE_REPOSITORY}-backend:${CANVAS_IMAGE_TAG}"
    CANVAS_WEB_IMAGE="${IMAGE_REPOSITORY}-web:${CANVAS_IMAGE_TAG}"
    backend_digest="$(docker image inspect "$CANVAS_BACKEND_IMAGE" --format '{{range .RepoDigests}}{{println .}}{{end}}' | awk -v repository="${IMAGE_REPOSITORY}-backend" 'index($0, repository "@sha256:") == 1 && $0 ~ /@sha256:[a-f0-9]{64}$/ { print; exit }')"
    web_digest="$(docker image inspect "$CANVAS_WEB_IMAGE" --format '{{range .RepoDigests}}{{println .}}{{end}}' | awk -v repository="${IMAGE_REPOSITORY}-web" 'index($0, repository "@sha256:") == 1 && $0 ~ /@sha256:[a-f0-9]{64}$/ { print; exit }')"
    validate_image_digest "$backend_digest" backend || fail "后端镜像未返回可验证的仓库 digest；拒绝继续使用可变 tag"
    validate_image_digest "$web_digest" web || fail "Web 镜像未返回可验证的仓库 digest；拒绝继续使用可变 tag"
    set_env_values .env \
        CANVAS_IMAGE_TAG "$CANVAS_IMAGE_TAG" \
        CANVAS_BACKEND_IMAGE "$backend_digest" \
        CANVAS_WEB_IMAGE "$web_digest"
    validate_deployment_digests
}

start_services() {
    step "启动 GHCR 网页与后端镜像"
    validate_deployment_digests
    docker compose --env-file .env -f "$COMPOSE_FILE" up -d --remove-orphans --wait --wait-timeout 600
}

print_result() {
    local local_ip
    local_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
    [[ -n "$local_ip" ]] || local_ip="服务器IP"

    printf '\n部署完成。\n'
    printf '访问地址：http://%s:%s\n' "$local_ip" "$CANVAS_HTTP_PORT"
    printf '安装目录：%s\n' "$INSTALL_DIR"
    printf '查看状态：cd %q && docker compose --env-file .env -f %s ps\n' "$INSTALL_DIR" "$COMPOSE_FILE"
    printf '\n首次打开后注册的第一个账号会自动成为管理员。公网长期使用前请配置 HTTPS。\n'
}

main() {
    load_configured_image_tag
    guard_legacy_release_before_write
    require_root
    step "安装服务器基础工具"
    install_packages
    install_docker
    login_ghcr
    prepare_environment
    download_compose
    pull_and_pin_images
    install_host_updater
    start_services
    print_result
}

main "$@"
