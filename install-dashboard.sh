#!/bin/bash

# LinuxGSM Web Dashboard - installation, update and uninstallation script
#
# Run directly via curl:
#   curl -sSL https://raw.githubusercontent.com/yourdawi/linuxgsm-dashboard/main/install-dashboard.sh | bash
#
# Installation modes:
#   (default)      latest signed release binary from GitHub (no Go or Git needed)
#   --source       build from the main branch (development build)
#   --dev          build from the dev branch (development build)
#   --local        build from the current directory (for developers)
#
# Other arguments: install | uninstall | status | --port <port>

# Terminal output colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

INSTALL_DIR="/opt/lgsm-dashboard"
BINARY="$INSTALL_DIR/lgsm-dashboard"
STATE_FILE="$INSTALL_DIR/.install-state"
SERVICE_NAME="lgsm-dashboard"
REPO="yourdawi/linuxgsm-dashboard"
REPO_URL="https://github.com/$REPO.git"

# Public key that signs release checksums. Must match releasePublicKey in main.go
# (the release workflow checks both).
RELEASE_PUBLIC_KEY="ySlp/QcA19j/qQXcyHmC+Ki1NvV7XSEYNl7z2z9c7P8="

# 1. Check for root privileges
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}[ERROR] This script must be run as root (e.g. using sudo bash).${NC}"
    exit 1
fi

# 2. Detect operating system
OS_TYPE="unknown"
if [ -f /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    case "$ID" in
        ubuntu|debian|raspbian|pop)
            OS_TYPE="debian"
            ;;
        centos|rhel|fedora|almalinux|rocky)
            OS_TYPE="redhat"
            ;;
        arch|manjaro)
            OS_TYPE="arch"
            ;;
        alpine)
            OS_TYPE="alpine"
            ;;
        opensuse*|sles|suse)
            OS_TYPE="suse"
            ;;
        *)
            if [[ "$ID_LIKE" =~ (debian|ubuntu) ]]; then
                OS_TYPE="debian"
            elif [[ "$ID_LIKE" =~ (rhel|fedora|centos) ]]; then
                OS_TYPE="redhat"
            elif [[ "$ID_LIKE" =~ (arch) ]]; then
                OS_TYPE="arch"
            elif [[ "$ID_LIKE" =~ (suse) ]]; then
                OS_TYPE="suse"
            fi
            ;;
    esac
fi

if [ "$OS_TYPE" == "unknown" ]; then
    if [ -f /etc/debian_version ]; then
        OS_TYPE="debian"
    elif [ -f /etc/redhat-release ]; then
        OS_TYPE="redhat"
    elif [ -f /etc/arch-release ]; then
        OS_TYPE="arch"
    elif [ -f /etc/alpine-release ]; then
        OS_TYPE="alpine"
    elif [ -f /etc/SuSE-release ]; then
        OS_TYPE="suse"
    fi
fi

# Helper to prompt user (interactive or fallback to tty)
prompt_user() {
    local prompt_msg="$1"
    local var_name="$2"
    if [ -t 0 ]; then
        read -r -p "$prompt_msg" "${var_name?}"
    else
        read -r -p "$prompt_msg" "${var_name?}" < /dev/tty
    fi
}

has_tty() {
    [ -t 0 ] || { [ -c /dev/tty ] && : < /dev/tty; } 2>/dev/null
}

# Helper to check if a package is installed
is_package_installed() {
    local pkg=$1
    if [ "$OS_TYPE" == "debian" ]; then
        dpkg -s "$pkg" &> /dev/null
    elif [ "$OS_TYPE" == "redhat" ] || [ "$OS_TYPE" == "suse" ]; then
        rpm -q "$pkg" &> /dev/null
    elif [ "$OS_TYPE" == "arch" ]; then
        pacman -Q "$pkg" &> /dev/null
    elif [ "$OS_TYPE" == "alpine" ]; then
        apk info -e "$pkg" &> /dev/null
    else
        return 1
    fi
}

APT_UPDATED="false"

# Helper function to install packages
install_package() {
    local pkg=$1
    echo -e "${YELLOW}[SYS] Installing required package: $pkg...${NC}"
    if [ "$OS_TYPE" == "debian" ]; then
        if [ "$APT_UPDATED" != "true" ]; then
            apt-get update -qq
            APT_UPDATED="true"
        fi
        apt-get install -y "$pkg" > /dev/null 2>&1
    elif [ "$OS_TYPE" == "redhat" ]; then
        if command -v dnf &> /dev/null; then
            dnf install -y "$pkg" > /dev/null 2>&1
        else
            yum install -y "$pkg" > /dev/null 2>&1
        fi
    elif [ "$OS_TYPE" == "arch" ]; then
        pacman -Sy --noconfirm "$pkg" > /dev/null 2>&1
    elif [ "$OS_TYPE" == "alpine" ]; then
        apk add --no-cache "$pkg" > /dev/null 2>&1
    elif [ "$OS_TYPE" == "suse" ]; then
        zypper --non-interactive install "$pkg" > /dev/null 2>&1
    else
        echo -e "${RED}[WARNING] Unknown system. Please install '$pkg' manually.${NC}"
    fi
}

# Helper function to uninstall packages
uninstall_package() {
    local pkg=$1
    echo -e "${YELLOW}[SYS] Removing package: $pkg...${NC}"
    if [ "$OS_TYPE" == "debian" ]; then
        apt-get purge -y "$pkg" > /dev/null 2>&1
        apt-get autoremove -y > /dev/null 2>&1
    elif [ "$OS_TYPE" == "redhat" ]; then
        if command -v dnf &> /dev/null; then
            dnf remove -y "$pkg" > /dev/null 2>&1
        else
            yum remove -y "$pkg" > /dev/null 2>&1
        fi
    elif [ "$OS_TYPE" == "arch" ]; then
        pacman -Rs --noconfirm "$pkg" > /dev/null 2>&1
    elif [ "$OS_TYPE" == "alpine" ]; then
        apk del "$pkg" > /dev/null 2>&1
    elif [ "$OS_TYPE" == "suse" ]; then
        zypper --non-interactive remove "$pkg" > /dev/null 2>&1
    fi
}

configure_firewall() {
    local action=$1
    if command -v ufw &> /dev/null && ufw status | grep -q "Status: active"; then
        if [ "$action" == "add" ]; then
            echo -e "${CYAN}[SYS] Configuring UFW firewall: Opening TCP port $PORT...${NC}"
            ufw allow "$PORT/tcp" comment "LinuxGSM Dashboard" > /dev/null
        else
            echo -e "${CYAN}[SYS] Removing UFW firewall rule for TCP port $PORT...${NC}"
            ufw delete allow "$PORT/tcp" > /dev/null
        fi
    elif command -v firewall-cmd &> /dev/null && systemctl is-active --quiet firewalld; then
        if [ "$action" == "add" ]; then
            echo -e "${CYAN}[SYS] Configuring Firewalld: Opening TCP port $PORT...${NC}"
            firewall-cmd --permanent --add-port="$PORT/tcp" > /dev/null
            firewall-cmd --reload > /dev/null
        else
            echo -e "${CYAN}[SYS] Removing Firewalld rule for TCP port $PORT...${NC}"
            firewall-cmd --permanent --remove-port="$PORT/tcp" > /dev/null
            firewall-cmd --reload > /dev/null
        fi
    fi
}

# ------------------------------------------------------------------
# Installation state (kept across updates)
# ------------------------------------------------------------------

STATE_GIT_PREEXISTED=""
STATE_GO_PREEXISTED=""
STATE_CURL_PREEXISTED=""
STATE_SUDO_PREEXISTED=""
STATE_PORT=""
STATE_INSTALLED_DEPS=""
STATE_INSTALL_DATE=""

load_state() {
    [ -f "$STATE_FILE" ] || return 1
    local GIT_PREEXISTED="" GO_PREEXISTED="" CURL_PREEXISTED="" SUDO_PREEXISTED="" PORT="" INSTALLED_DEPS="" INSTALL_DATE=""
    # shellcheck disable=SC1090
    source "$STATE_FILE"
    STATE_GIT_PREEXISTED="$GIT_PREEXISTED"
    STATE_GO_PREEXISTED="$GO_PREEXISTED"
    STATE_CURL_PREEXISTED="$CURL_PREEXISTED"
    STATE_SUDO_PREEXISTED="$SUDO_PREEXISTED"
    STATE_PORT="$PORT"
    STATE_INSTALLED_DEPS="$INSTALLED_DEPS"
    STATE_INSTALL_DATE="$INSTALL_DATE"
    return 0
}

# Records whether a tool existed before the first installation. Later runs never change an
# existing "true"/"false" entry, so uninstalling removes exactly what this script installed.
track_tool() {
    local pkg=$1 current=$2
    if [ -n "$current" ]; then
        echo "$current"
    elif is_package_installed "$pkg" || command -v "$pkg" &> /dev/null; then
        echo "true"
    else
        echo "false"
    fi
}

write_state() {
    local installed_deps="$1" mode="$2" version="$3"
    cat <<EOT > "$STATE_FILE"
GIT_PREEXISTED=$STATE_GIT_PREEXISTED
GO_PREEXISTED=$STATE_GO_PREEXISTED
CURL_PREEXISTED=$STATE_CURL_PREEXISTED
SUDO_PREEXISTED=$STATE_SUDO_PREEXISTED
PORT=$PORT
INSTALLED_DEPS="$installed_deps"
INSTALL_DATE="${STATE_INSTALL_DATE:-$(date '+%Y-%m-%d %H:%M:%S')}"
LAST_UPDATE="$(date '+%Y-%m-%d %H:%M:%S')"
INSTALL_MODE="$mode"
INSTALLED_VERSION="$version"
EOT
    chmod 600 "$STATE_FILE"
}

# ------------------------------------------------------------------
# Release download
# ------------------------------------------------------------------

download() {
    local url=$1 out=$2
    if command -v curl &> /dev/null; then
        curl -fsSL --retry 3 --connect-timeout 15 -o "$out" "$url"
    else
        wget -q --tries=3 --timeout=30 -O "$out" "$url"
    fi
}

# Verifies the Ed25519 signature of SHA256SUMS with OpenSSL 3. Older OpenSSL versions cannot
# verify Ed25519 signatures from the command line; then only the checksum is verified.
verify_signature() {
    local sums=$1 sig=$2 work
    if [ -z "$RELEASE_PUBLIC_KEY" ]; then
        echo -e "${YELLOW}[WARNING] No release signing key configured in this installer, verifying the checksum only.${NC}"
        return 0
    fi
    if ! command -v openssl &> /dev/null || ! openssl version 2>/dev/null | grep -qE '^OpenSSL 3'; then
        echo -e "${YELLOW}[WARNING] OpenSSL 3 not available, verifying the checksum only.${NC}"
        return 0
    fi
    work=$(mktemp -d)
    # DER header of an Ed25519 SubjectPublicKeyInfo followed by the 32 byte key.
    { printf '\060\052\060\005\006\003\053\145\160\003\041\000'; printf '%s' "$RELEASE_PUBLIC_KEY" | base64 -d; } > "$work/pub.der"
    base64 -d < "$sig" > "$work/sig.bin" 2>/dev/null
    if openssl pkey -pubin -inform DER -in "$work/pub.der" -out "$work/pub.pem" 2>/dev/null &&
       openssl pkeyutl -verify -pubin -inkey "$work/pub.pem" -rawin -in "$sums" -sigfile "$work/sig.bin" > /dev/null 2>&1; then
        rm -rf "$work"
        echo -e "${GREEN}[OK] Release signature verified.${NC}"
        return 0
    fi
    rm -rf "$work"
    echo -e "${RED}[ERROR] The release signature is invalid. Aborting.${NC}"
    return 1
}

release_asset_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) echo "" ;;
    esac
}

# Downloads and verifies the latest release binary to $1. Prints the version on success.
fetch_release_binary() {
    local target=$1 arch asset tag base work
    arch=$(release_asset_arch)
    asset="lgsm-dashboard-linux-$arch"

    tag=$(download "https://api.github.com/repos/$REPO/releases/latest" /dev/stdout 2>/dev/null \
        | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/')
    if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${RED}[ERROR] Could not determine the latest release (got '$tag').${NC}" >&2
        return 1
    fi
    echo -e "${CYAN}[SYS] Downloading release $tag ($asset)...${NC}" >&2

    base="https://github.com/$REPO/releases/download/$tag"
    work=$(mktemp -d)
    if ! download "$base/$asset" "$work/$asset" || ! download "$base/SHA256SUMS" "$work/SHA256SUMS" ||
       ! download "$base/SHA256SUMS.sig" "$work/SHA256SUMS.sig"; then
        echo -e "${RED}[ERROR] Download of release $tag failed. The release may not contain signed binaries yet.${NC}" >&2
        rm -rf "$work"
        return 1
    fi

    if ! verify_signature "$work/SHA256SUMS" "$work/SHA256SUMS.sig" >&2; then
        rm -rf "$work"
        return 1
    fi
    if ! (cd "$work" && grep -E "[[:space:]]\*?$asset\$" SHA256SUMS | sha256sum -c --status -); then
        echo -e "${RED}[ERROR] Checksum mismatch for $asset. Aborting.${NC}" >&2
        rm -rf "$work"
        return 1
    fi
    echo -e "${GREEN}[OK] Checksum verified.${NC}" >&2

    chmod 0755 "$work/$asset"
    if [ "$("$work/$asset" -version 2>/dev/null)" != "${tag#v}" ]; then
        echo -e "${RED}[ERROR] The downloaded binary does not report version ${tag#v}. Aborting.${NC}" >&2
        rm -rf "$work"
        return 1
    fi
    mv -f "$work/$asset" "$target"
    rm -rf "$work"
    echo "${tag#v}"
}

# ------------------------------------------------------------------
# Source build
# ------------------------------------------------------------------

# Builds the dashboard from source. $1 = main | dev | local, $2 = output file.
# Prints the version on success.
build_from_source() {
    local source=$1 target=$2 src version
    src="$INSTALL_DIR/src"
    rm -rf "$src"
    mkdir -p "$src"

    if [ "$source" == "local" ]; then
        if [ ! -f ./main.go ] || [ ! -d ./backend ] || [ ! -d ./ui ]; then
            echo -e "${RED}[ERROR] --local must be run from the repository directory.${NC}" >&2
            return 1
        fi
        echo -e "${CYAN}[SYS] Copying source from $(pwd)...${NC}" >&2
        cp -r ./main.go ./go.mod ./go.sum ./backend ./ui "$src/"
        version="dev-local"
        if command -v git &> /dev/null && git rev-parse --short HEAD &> /dev/null; then
            version="dev-local-$(git rev-parse --short HEAD)"
        fi
    else
        echo -e "${CYAN}[SYS] Downloading source (branch: $source)...${NC}" >&2
        if ! git clone --quiet --depth 1 -b "$source" "$REPO_URL" "$src" > /dev/null 2>&1; then
            echo -e "${RED}[ERROR] Download of branch $source failed.${NC}" >&2
            return 1
        fi
        version="dev-$source-$(git -C "$src" rev-parse --short HEAD)"
    fi

    echo -e "${YELLOW}[SYS] Compiling $version (this can take a minute)...${NC}" >&2
    if ! (cd "$src" && go build -trimpath -ldflags "-X main.Version=$version" -o "$target" .) >&2; then
        echo -e "${RED}[ERROR] Compilation failed. Go $(go env GOVERSION 2>/dev/null) is installed; Go 1.20 or newer is recommended.${NC}" >&2
        return 1
    fi
    echo "$version"
}

# Older versions kept the Git checkout directly in the installation directory.
remove_legacy_source_layout() {
    local item
    for item in main.go main_test.go handlers.go go.mod go.sum backend ui tools .git .github lgsm-dashboard.new; do
        if [ -e "$INSTALL_DIR/$item" ]; then
            rm -rf "${INSTALL_DIR:?}/$item"
        fi
    done
}

# ------------------------------------------------------------------
# Install / update
# ------------------------------------------------------------------

install_lgsm_dependencies() {
    local -n deps_ref=$1

    try_install() {
        local primary="$1"
        if [ "$OS_TYPE" == "debian" ]; then
            if apt-cache show "$primary" > /dev/null 2>&1; then
                apt-get install -y "$primary" > /dev/null 2>&1
            fi
        elif [ "$OS_TYPE" == "redhat" ]; then
            if command -v dnf &> /dev/null; then
                dnf install -y "$primary" > /dev/null 2>&1
            else
                yum install -y "$primary" > /dev/null 2>&1
            fi
        fi
    }

    echo -e "${YELLOW}[SYS] Checking LinuxGSM game server dependencies...${NC}"
    local pkg
    if [ "$OS_TYPE" == "debian" ]; then
        dpkg --add-architecture i386 > /dev/null 2>&1
        apt-get update -y > /dev/null 2>&1
        APT_UPDATED="true"

        local LGSM_DEPS=(
            bc binutils bsdmainutils bzip2 ca-certificates cpio curl
            distro-info file gzip hostname jq lib32gcc-s1 lib32stdc++6
            netcat-openbsd pigz python3 rclone tmux unzip util-linux uuid-runtime
            wget xz-utils libxml2-utils
        )
        for pkg in "${LGSM_DEPS[@]}"; do
            if ! is_package_installed "$pkg"; then
                try_install "$pkg"
                is_package_installed "$pkg" && deps_ref+=("$pkg")
            fi
        done

        local pair primary fallback
        for pair in "libsdl2-2.0-0:i386 libsdl2-2.0-0" "libncurses5 libncurses6" "libncursesw5 libncursesw6"; do
            primary=${pair% *}
            fallback=${pair#* }
            if is_package_installed "$primary" || is_package_installed "$fallback"; then
                continue
            fi
            try_install "$primary"
            if is_package_installed "$primary"; then
                deps_ref+=("$primary")
            else
                try_install "$fallback"
                is_package_installed "$fallback" && deps_ref+=("$fallback")
            fi
        done
    elif [ "$OS_TYPE" == "redhat" ]; then
        local LGSM_DEPS=(bc binutils bzip2 curl file gzip jq rclone tmux unzip wget tar)
        for pkg in "${LGSM_DEPS[@]}"; do
            if ! is_package_installed "$pkg"; then
                try_install "$pkg"
                is_package_installed "$pkg" && deps_ref+=("$pkg")
            fi
        done
    else
        echo -e "${YELLOW}[WARNING] Automatic dependency installation is only available for Debian/Ubuntu and RHEL based systems. LinuxGSM will list missing packages during the game server installation.${NC}"
    fi
    echo -e "${GREEN}[OK] LinuxGSM dependencies checked.${NC}"
}

# Port the running dashboard uses (from the service file), empty if not installed.
installed_service_port() {
    local unit="/etc/systemd/system/$SERVICE_NAME.service"
    [ -f "$unit" ] && sed -nE 's/.*-port ([0-9]+).*/\1/p' "$unit" | head -n1
}

port_in_use() {
    local port=$1
    if command -v ss &> /dev/null; then
        ss -tuln | grep -qE "[:.]${port}[[:space:]]"
    elif command -v netstat &> /dev/null; then
        netstat -tuln | grep -qE "[:.]${port}[[:space:]]"
    else
        return 1
    fi
}

# $1 = release | main | dev | local
do_install() {
    local mode="${1:-release}"
    local is_update="false"
    [ -x "$BINARY" ] && is_update="true"

    echo -e "\n${BLUE}==================================================================${NC}"
    if [ "$is_update" == "true" ]; then
        echo -e "${MAGENTA}                  Updating installation ($mode)...                ${NC}"
    else
        echo -e "${MAGENTA}                  Starting installation ($mode)...                ${NC}"
    fi
    echo -e "${BLUE}==================================================================${NC}"

    # 1. Pre-flight checks
    echo -e "${CYAN}[SYS] Running pre-flight system checks...${NC}"
    local arch
    arch=$(uname -m)
    if [ -z "$(release_asset_arch)" ]; then
        echo -e "${RED}[ERROR] Unsupported architecture: $arch.${NC}"
        return 1
    fi
    if ! command -v systemctl &> /dev/null || [ ! -d /run/systemd/system ]; then
        echo -e "${RED}[ERROR] systemd is required (the dashboard runs as a systemd service). Distributions without systemd, such as Alpine with OpenRC, are not supported.${NC}"
        return 1
    fi
    if [ "$(release_asset_arch)" == "arm64" ]; then
        echo -e "${YELLOW}[WARNING] ARM64 detected. The dashboard runs on ARM, but most LinuxGSM game servers require x86_64.${NC}"
    fi

    if [ -f /proc/meminfo ] && [ "$is_update" != "true" ]; then
        local total_ram
        total_ram=$(awk '/MemTotal/ {print $2}' /proc/meminfo)
        if [ "$total_ram" -lt 2000000 ]; then
            echo -e "${YELLOW}[WARNING] Your system has less than 2GB of RAM. Gameservers might perform poorly or fail to run.${NC}"
            if has_tty; then
                local ram_confirm=""
                prompt_user "Do you want to continue anyway? [y/N]: " ram_confirm
                if [[ ! "$ram_confirm" =~ ^[yY]$ ]]; then
                    echo -e "${YELLOW}[INFO] Installation aborted.${NC}"
                    return 1
                fi
            fi
        fi
    fi

    load_state
    mkdir -p "$INSTALL_DIR"

    # 2. Port: keep the port of an existing installation unless --port was given.
    local current_port
    current_port=$(installed_service_port)
    if [ "$PORT_FROM_ARGS" != "true" ]; then
        PORT="${current_port:-${STATE_PORT:-$PORT}}"
        if [ "$is_update" != "true" ] && has_tty; then
            local user_port=""
            prompt_user "Please enter the port for the dashboard [default: $PORT]: " user_port
            if [ -n "$user_port" ]; then
                if ! [[ "$user_port" =~ ^[0-9]+$ ]] || [ "$user_port" -lt 1 ] || [ "$user_port" -gt 65535 ]; then
                    echo -e "${RED}[ERROR] Invalid port entered. Using $PORT.${NC}"
                else
                    PORT="$user_port"
                fi
            fi
        fi
    fi
    # The running dashboard itself occupies its own port during an update.
    if [ "$PORT" != "$current_port" ] && port_in_use "$PORT"; then
        echo -e "${RED}[ERROR] Port $PORT is already in use on this system. Please choose another port.${NC}"
        return 1
    fi

    # 3. Tools
    STATE_CURL_PREEXISTED=$(track_tool curl "$STATE_CURL_PREEXISTED")
    STATE_SUDO_PREEXISTED=$(track_tool sudo "$STATE_SUDO_PREEXISTED")
    local go_pkg="golang"
    if [ "$OS_TYPE" == "arch" ] || [ "$OS_TYPE" == "alpine" ] || [ "$OS_TYPE" == "suse" ]; then
        go_pkg="go"
    fi

    # shellcheck disable=SC2206
    local installed_packages=($STATE_INSTALLED_DEPS)
    add_installed() {
        local p
        for p in "${installed_packages[@]}"; do [ "$p" == "$1" ] && return; done
        installed_packages+=("$1")
    }

    command -v curl &> /dev/null || { install_package curl; add_installed curl; }
    # LinuxGSM's dependency installer needs sudo (granted temporarily during game installation).
    is_package_installed sudo || command -v sudo &> /dev/null || { install_package sudo; add_installed sudo; }

    if [ "$mode" != "release" ]; then
        STATE_GIT_PREEXISTED=$(track_tool git "$STATE_GIT_PREEXISTED")
        STATE_GO_PREEXISTED=$(track_tool go "$STATE_GO_PREEXISTED")
        if [ "$mode" != "local" ] && ! command -v git &> /dev/null; then
            install_package git
            add_installed git
        fi
        if ! command -v go &> /dev/null; then
            install_package "$go_pkg"
            add_installed "$go_pkg"
        fi
        if ! command -v go &> /dev/null; then
            echo -e "${RED}[ERROR] Go could not be installed. Aborting.${NC}"
            return 1
        fi
    fi

    local new_deps=()
    install_lgsm_dependencies new_deps
    local p
    for p in "${new_deps[@]}"; do add_installed "$p"; done

    # 4. Dashboard binary
    local new_binary="$INSTALL_DIR/lgsm-dashboard.download" version
    rm -f "$new_binary"
    if [ "$mode" == "release" ]; then
        version=$(fetch_release_binary "$new_binary") || return 1
    else
        version=$(build_from_source "$mode" "$new_binary") || return 1
    fi

    remove_legacy_source_layout
    [ -f "$BINARY" ] && cp -p "$BINARY" "$BINARY.old"
    # rename() replaces the binary atomically, even while the old one is running.
    mv -f "$new_binary" "$BINARY"
    chmod 0755 "$BINARY"
    mkdir -p "$INSTALL_DIR/config"
    chmod 700 "$INSTALL_DIR/config"
    echo -e "${GREEN}[OK] Dashboard $version installed to $BINARY.${NC}"

    write_state "${installed_packages[*]}" "$mode" "$version"

    # 5. systemd service (with security sandboxing)
    echo -e "${CYAN}[SYS] Writing systemd service...${NC}"
    cat <<EOT > "/etc/systemd/system/$SERVICE_NAME.service"
[Unit]
Description=LinuxGSM Web Dashboard
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$BINARY -port $PORT -config-dir $INSTALL_DIR/config
Restart=always
RestartSec=5

# Security Sandboxing
ProtectSystem=true
ProtectHome=false
# PrivateTmp must be false so tmux sockets can be shared between the daemon and gameserver users
PrivateTmp=false
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_DAC_OVERRIDE CAP_KILL CAP_SETUID CAP_SETGID CAP_CHOWN CAP_FOWNER CAP_DAC_READ_SEARCH CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOT

    # 6. Firewall (new installations only)
    if [ "$is_update" != "true" ]; then
        local open_fw="false"
        if command -v ufw &> /dev/null && ufw status | grep -q "Status: active"; then
            open_fw="true"
        elif command -v firewall-cmd &> /dev/null && systemctl is-active --quiet firewalld; then
            open_fw="true"
        fi
        if [ "$open_fw" == "true" ]; then
            local fw_confirm="y"
            if has_tty; then
                fw_confirm=""
                prompt_user "Active firewall detected. Do you want to automatically open port $PORT? [Y/n]: " fw_confirm
            fi
            if [[ "$fw_confirm" =~ ^[yY]?$ ]]; then
                configure_firewall "add"
            fi
        fi
    fi

    # 7. Start
    echo -e "${CYAN}[SYS] Starting systemd service...${NC}"
    systemctl daemon-reload
    systemctl enable "$SERVICE_NAME" > /dev/null 2>&1
    systemctl restart "$SERVICE_NAME"
    sleep 3

    if ! systemctl is-active --quiet "$SERVICE_NAME"; then
        echo -e "${RED}[ERROR] The service did not start. Check: journalctl -u $SERVICE_NAME -n 50${NC}"
        if [ -f "$BINARY.old" ]; then
            echo -e "${YELLOW}[INFO] The previous binary is available as $BINARY.old.${NC}"
        fi
        return 1
    fi

    local password=""
    if [ "$is_update" != "true" ]; then
        password=$(journalctl -u "$SERVICE_NAME" -n 100 --no-pager | grep -oE "Password: [A-Za-z0-9]+" | tail -n 1 | cut -d' ' -f2)
    fi

    local server_ip public_ip
    server_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
    [ -z "$server_ip" ] && server_ip="YOUR-SERVER-IP"
    public_ip=$(curl -s --max-time 2 https://api.ipify.org 2>/dev/null || echo "")

    echo -e "\n${BLUE}==================================================================${NC}"
    if [ "$is_update" == "true" ]; then
        echo -e "${GREEN}      LinuxGSM Web Dashboard updated to $version${NC}"
    else
        echo -e "${GREEN}      LinuxGSM Web Dashboard successfully installed ($version)${NC}"
    fi
    echo -e "${BLUE}==================================================================${NC}"
    if [ -n "$public_ip" ] && [ "$public_ip" != "$server_ip" ]; then
        echo -e "  URL (Local):  ${CYAN}http://$server_ip:$PORT${NC}"
        echo -e "  URL (Public): ${CYAN}http://$public_ip:$PORT${NC}"
    else
        echo -e "  URL:          ${CYAN}http://$server_ip:$PORT${NC}"
    fi
    if [ "$is_update" != "true" ]; then
        echo -e "  Username:  ${CYAN}admin${NC}"
        if [ -n "$password" ]; then
            echo -e "  Password:  ${GREEN}$password${NC}  <-- Please change immediately!"
        else
            echo -e "  Password:  ${RED}Could not read. Run 'journalctl -u $SERVICE_NAME' to view.${NC}"
        fi
    fi
    if [ "$mode" != "release" ]; then
        echo -e "  ${YELLOW}Development build: the dashboard does not offer release updates. Run this script without --source/--dev/--local to switch back to the release.${NC}"
    fi
    echo -e "${BLUE}==================================================================${NC}"
}

# ------------------------------------------------------------------
# Uninstall
# ------------------------------------------------------------------

do_uninstall() {
    echo -e "\n${RED}==================================================================${NC}"
    echo -e "${RED}             Starting Uninstallation...                           ${NC}"
    echo -e "${RED}==================================================================${NC}"

    local confirm="" rm_pkg=""
    if ! prompt_user "Are you sure you want to delete the dashboard and all configurations? [y/N]: " confirm; then
        echo -e "${RED}[ERROR] Input stream closed. Exiting...${NC}"
        exit 1
    fi
    if [[ ! "$confirm" =~ ^[yY]$ ]]; then
        echo -e "${YELLOW}[INFO] Uninstallation cancelled.${NC}"
        return
    fi

    local have_state="false"
    if load_state; then
        have_state="true"
        echo -e "${GREEN}[INFO] Installation log loaded.${NC}"
    else
        echo -e "${YELLOW}[WARNING] No .install-state log file found.${NC}"
    fi
    PORT="$(installed_service_port)"
    PORT="${PORT:-${STATE_PORT:-8080}}"

    # 1. Stop and remove systemd service
    echo -e "${CYAN}[SYS] Stopping and removing systemd service...${NC}"
    systemctl stop "$SERVICE_NAME" > /dev/null 2>&1
    systemctl disable "$SERVICE_NAME" > /dev/null 2>&1
    rm -f "/etc/systemd/system/$SERVICE_NAME.service"
    systemctl daemon-reload

    # 2. Remove firewall rules
    configure_firewall "remove"

    # 3. Clean up system packages if requested
    local remove_packages="false"
    if [ "$have_state" == "true" ]; then
        if prompt_user "Do you want to uninstall system packages that were installed by this script? [y/N]: " rm_pkg; then
            if [[ "$rm_pkg" =~ ^[yY]$ ]]; then
                remove_packages="true"
            fi
        fi
    else
        echo -e "${YELLOW}[WARNING] System packages will be kept to prevent issues since no log was found.${NC}"
    fi

    if [ "$remove_packages" == "true" ] && [ -n "$STATE_INSTALLED_DEPS" ]; then
        local pkg
        for pkg in $STATE_INSTALLED_DEPS; do
            uninstall_package "$pkg"
        done
    elif [ "$remove_packages" != "true" ]; then
        echo -e "${GREEN}[INFO] Keeping all system packages as requested.${NC}"
    fi

    # 4. Delete installation directory
    if [ -d "$INSTALL_DIR" ]; then
        echo -e "${CYAN}[SYS] Deleting installation directory: $INSTALL_DIR...${NC}"
        rm -rf "$INSTALL_DIR"
    fi

    echo -e "\n${GREEN}[OK] LinuxGSM Web Dashboard has been completely removed.${NC}"
    echo -e "${YELLOW}[INFO] Game server users and their files were not touched.${NC}"
}

# Status and diagnosis information
show_status() {
    echo -e "\n${BLUE}==================================================================${NC}"
    echo -e "${CYAN}                  Status & System Information                     ${NC}"
    echo -e "${BLUE}==================================================================${NC}"

    if systemctl is-active --quiet "$SERVICE_NAME"; then
        echo -e "Service Status:    ${GREEN}ACTIVE (Running)${NC}"
    else
        echo -e "Service Status:    ${RED}INACTIVE (Stopped / Not installed)${NC}"
    fi

    if [ -x "$BINARY" ]; then
        echo -e "Installation Path: ${GREEN}$INSTALL_DIR${NC}"
        echo -e "Version:           ${GREEN}$("$BINARY" -version 2>/dev/null)${NC}"
    else
        echo -e "Installation Path: ${RED}Not installed${NC}"
    fi

    echo -e "\n${YELLOW}System Load:${NC}"
    echo "  Load Average:   $(cut -d' ' -f1-3 /proc/loadavg 2>/dev/null)"
    free -h | awk '/Mem:/ {print "  Memory Usage:   " $3 "/" $2}'
    df -h / | awk 'NR==2 {print "  Disk Usage:     " $3 "/" $2 " used"}'

    if [ -f "$STATE_FILE" ]; then
        echo -e "\n${YELLOW}Installation Log:${NC}"
        sed 's/^/  /' "$STATE_FILE"
    fi
    echo -e "${BLUE}==================================================================${NC}"
}

# ------------------------------------------------------------------
# Arguments
# ------------------------------------------------------------------

ACTION=""
PORT="8080"
PORT_FROM_ARGS="false"
MODE="release"

while [[ $# -gt 0 ]]; do
    case $1 in
        install|--install)
            ACTION="install"
            shift
            ;;
        uninstall|--uninstall)
            ACTION="uninstall"
            shift
            ;;
        status|--status)
            ACTION="status"
            shift
            ;;
        dev|--dev)
            MODE="dev"
            shift
            ;;
        source|--source)
            MODE="main"
            shift
            ;;
        local|--local)
            MODE="local"
            shift
            ;;
        --port)
            PORT="$2"
            PORT_FROM_ARGS="true"
            shift 2
            ;;
        *)
            if [ -z "$ACTION" ]; then
                ACTION="$1"
            fi
            shift
            ;;
    esac
done

# Validate port format
if ! [[ "$PORT" =~ ^[0-9]+$ ]] || [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
    echo -e "${RED}[ERROR] Invalid port: $PORT. Must be a number between 1 and 65535.${NC}"
    exit 1
fi

if [ -n "$ACTION" ] || [ "$MODE" != "release" ]; then
    case ${ACTION:-install} in
        install)
            do_install "$MODE"
            exit $?
            ;;
        uninstall)
            do_uninstall
            exit 0
            ;;
        status)
            show_status
            exit 0
            ;;
        *)
            echo -e "${RED}[ERROR] Unknown argument: $ACTION${NC}"
            echo -e "Usage: $0 [install|uninstall|status] [--port <port>] [--source|--dev|--local]"
            exit 1
            ;;
    esac
fi

# Non-interactive shell (no terminal): install the release directly
if ! has_tty; then
    echo -e "${YELLOW}[INFO] Non-interactive shell detected (no /dev/tty). Installing the latest release...${NC}"
    do_install release
    exit $?
fi

# Menu loop (interactive only)
choice=""
while true; do
    echo -e "\n${BLUE}==================================================================${NC}"
    echo -e "${CYAN}             LinuxGSM Web Dashboard - Management Menu             ${NC}"
    echo -e "${BLUE}==================================================================${NC}"
    echo -e "  1) Install / update (latest release)"
    echo -e "  2) Install / update from source: main branch (development build)"
    echo -e "  3) Install / update from source: dev branch (development build)"
    echo -e "  4) Completely uninstall dashboard"
    echo -e "  5) Show status & system info"
    echo -e "  6) Exit"
    echo -e "${BLUE}==================================================================${NC}"
    if ! prompt_user "Please choose an option [1-6]: " choice; then
        echo -e "${RED}[ERROR] Input stream closed. Exiting...${NC}"
        exit 1
    fi

    case $choice in
        1) do_install release ;;
        2) do_install main ;;
        3) do_install dev ;;
        4) do_uninstall ;;
        5) show_status ;;
        6)
            echo -e "${GREEN}Goodbye!${NC}"
            exit 0
            ;;
        *)
            echo -e "${RED}[ERROR] Invalid option. Please choose 1-6.${NC}"
            ;;
    esac
done
