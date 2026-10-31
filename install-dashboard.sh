#!/bin/bash

# LinuxGSM Web Dashboard - installation, update and uninstallation script
#
# Run directly via curl:
#   curl -sSL https://raw.githubusercontent.com/yourdawi/linuxgsm-dashboard/main/install-dashboard.sh | bash
#
# Installation modes:
#   (default)      latest signed release binary from GitHub (no Go or Git needed)
#                  Source builds use the installed Go if it is new enough, otherwise a temporary
#                  official Go release from go.dev (checksum verified, removed after the build).
#   --source       build from the main branch (development build)
#   --dev          build from the dev branch (development build)
#   --local        build from the current directory (for developers)
#
# Other arguments: install | uninstall | status | --port <port>
#
# Node of another dashboard (agent, see Settings > Nodes in the dashboard):
#   --agent --join '<pairing command>' [--mode read-only|read-write]
#     (or --join <url> --token <token> --ca-fingerprint <sha256:...>)
#     [--fastdl-port <port> --fastdl-url <url>]   (FastDL served by the node itself)
#     [--insecure-skip-signature]   (only without OpenSSL 3: rely on the checksum of a release)

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
# A node of another dashboard runs the same binary as agent under its own service.
AGENT_SERVICE_NAME="lgsm-dashboard-agent"
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

STATE_ROLE=""
STATE_GIT_PREEXISTED=""
STATE_GO_PREEXISTED=""
STATE_CURL_PREEXISTED=""
STATE_SUDO_PREEXISTED=""
STATE_PORT=""
STATE_INSTALLED_DEPS=""
STATE_INSTALL_DATE=""
STATE_FASTDL_PORT=""
STATE_FASTDL_URL=""

load_state() {
    [ -f "$STATE_FILE" ] || return 1
    local ROLE="" GIT_PREEXISTED="" GO_PREEXISTED="" CURL_PREEXISTED="" SUDO_PREEXISTED="" PORT="" INSTALLED_DEPS="" INSTALL_DATE="" FASTDL_PORT="" FASTDL_URL=""
    # shellcheck disable=SC1090
    source "$STATE_FILE"
    STATE_ROLE="${ROLE:-dashboard}"
    STATE_GIT_PREEXISTED="$GIT_PREEXISTED"
    STATE_GO_PREEXISTED="$GO_PREEXISTED"
    STATE_CURL_PREEXISTED="$CURL_PREEXISTED"
    STATE_SUDO_PREEXISTED="$SUDO_PREEXISTED"
    STATE_PORT="$PORT"
    STATE_INSTALLED_DEPS="$INSTALLED_DEPS"
    STATE_INSTALL_DATE="$INSTALL_DATE"
    STATE_FASTDL_PORT="$FASTDL_PORT"
    STATE_FASTDL_URL="$FASTDL_URL"
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
    local installed_deps="$1" mode="$2" version="$3" role="${4:-dashboard}"
    cat <<EOT > "$STATE_FILE"
ROLE=$role
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
FASTDL_PORT="$STATE_FASTDL_PORT"
FASTDL_URL="$STATE_FASTDL_URL"
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
    # OpenSSL 3 or later verifies Ed25519 from the command line.
    if ! command -v openssl &> /dev/null || ! openssl version 2>/dev/null | grep -qE '^OpenSSL ([3-9]|[1-9][0-9])[.]'; then
        if [ "$INSECURE_SKIP_SIGNATURE" == "true" ]; then
            echo -e "${YELLOW}[WARNING] OpenSSL 3 not available, verifying the checksum only (--insecure-skip-signature).${NC}"
            return 0
        fi
        echo -e "${RED}[ERROR] OpenSSL 3 or later is needed to verify the release signature. Install it, or run this script with --insecure-skip-signature to rely on the checksum only.${NC}"
        return 1
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
# Go toolchain for builds from source
# ------------------------------------------------------------------

# Go binary used for the build and the temporary directory of a downloaded Go release.
GO_BIN=""
GO_WORK=""

# Prints the minimum Go version from the go directive of $1/go.mod (for example 1.27).
required_go_version() {
    sed -n -E 's/^go[[:space:]]+([0-9]+\.[0-9]+(\.[0-9]+)?)[[:space:]]*$/\1/p' "$1/go.mod" | head -n1
}

# version_at_least <have> <want>
version_at_least() {
    [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]
}

# Sets GO_BIN to a Go of at least version $1: the Go installed on the system when it is new
# enough, otherwise the current official release from go.dev, checked against the SHA-256
# checksum go.dev publishes and unpacked into a temporary directory (removed by cleanup_go).
# Distribution packages are not used because they are often several releases behind.
prepare_go() {
    local required=$1 have arch entry version sha file
    if command -v go &> /dev/null; then
        have=$(cd / && GOTOOLCHAIN=local go env GOVERSION 2>/dev/null)
        have=${have#go}
        if [ -n "$have" ] && version_at_least "$have" "$required"; then
            GO_BIN=$(command -v go)
            echo -e "${CYAN}[SYS] Using the installed Go $have.${NC}" >&2
            return 0
        fi
        echo -e "${YELLOW}[SYS] The installed Go ${have:-(unknown version)} is older than $required, using the official Go release instead.${NC}" >&2
    fi

    arch=$(release_asset_arch)
    if [ -z "$arch" ]; then
        echo -e "${RED}[ERROR] No official Go release for architecture $(uname -m).${NC}" >&2
        return 1
    fi
    # The list holds the stable releases, newest first. The first archive for this platform is
    # the current release.
    entry=$(download "https://go.dev/dl/?mode=json" /dev/stdout 2>/dev/null | tr -d ' \t\r\n' |
        grep -o "\"filename\":\"go[0-9.]*\.linux-$arch\.tar\.gz\"[^}]*\"kind\":\"archive\"" | head -n1)
    version=$(sed -E 's/.*"version":"go([0-9.]+)".*/\1/' <<< "$entry")
    sha=$(sed -E 's/.*"sha256":"([0-9a-f]{64})".*/\1/' <<< "$entry")
    if ! [[ "$version" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || ! [[ "$sha" =~ ^[0-9a-f]{64}$ ]]; then
        echo -e "${RED}[ERROR] Could not determine the current Go release from go.dev.${NC}" >&2
        return 1
    fi
    if ! version_at_least "$version" "$required"; then
        echo -e "${RED}[ERROR] The current Go release $version is older than the required Go $required.${NC}" >&2
        return 1
    fi

    GO_WORK=$(mktemp -d)
    file="go$version.linux-$arch.tar.gz"
    echo -e "${CYAN}[SYS] Downloading Go $version ($file)...${NC}" >&2
    if ! download "https://go.dev/dl/$file" "$GO_WORK/$file"; then
        echo -e "${RED}[ERROR] Download of Go $version failed.${NC}" >&2
        cleanup_go
        return 1
    fi
    if ! echo "$sha  $GO_WORK/$file" | sha256sum -c --status -; then
        echo -e "${RED}[ERROR] Checksum mismatch for $file. Aborting.${NC}" >&2
        cleanup_go
        return 1
    fi
    if ! tar --no-same-owner -C "$GO_WORK" -xzf "$GO_WORK/$file"; then
        echo -e "${RED}[ERROR] Unpacking $file failed.${NC}" >&2
        cleanup_go
        return 1
    fi
    rm -f "${GO_WORK:?}/$file"
    GO_BIN="$GO_WORK/go/bin/go"
    echo -e "${GREEN}[OK] Go $version verified.${NC}" >&2
}

# Removes a downloaded Go release including its module and build cache.
cleanup_go() {
    if [ -n "$GO_WORK" ]; then
        rm -rf "$GO_WORK"
    fi
    GO_WORK=""
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

    local required
    required=$(required_go_version "$src")
    if [ -z "$required" ]; then
        echo -e "${RED}[ERROR] go.mod of the source has no go directive.${NC}" >&2
        return 1
    fi
    prepare_go "$required" || return 1

    echo -e "${YELLOW}[SYS] Compiling $version (this can take a minute)...${NC}" >&2
    # GOTOOLCHAIN=local: never switch to yet another downloaded toolchain. A downloaded Go keeps
    # its module and build cache in its temporary directory, so nothing stays behind.
    local build_ok="true"
    if [ -n "$GO_WORK" ]; then
        (cd "$src" && GOTOOLCHAIN=local GOPATH="$GO_WORK/gopath" GOCACHE="$GO_WORK/cache" \
            "$GO_BIN" build -trimpath -ldflags "-X main.Version=$version" -o "$target" .) >&2 || build_ok="false"
    else
        (cd "$src" && GOTOOLCHAIN=local "$GO_BIN" build -trimpath -ldflags "-X main.Version=$version" -o "$target" .) >&2 || build_ok="false"
    fi
    cleanup_go
    if [ "$build_ok" != "true" ]; then
        echo -e "${RED}[ERROR] Compilation failed.${NC}" >&2
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

# Packages the installer adds for LinuxGSM game servers (install_lgsm_dependencies).
LGSM_GAME_PACKAGES=" bc binutils bsdmainutils bzip2 ca-certificates cpio curl distro-info file gzip hostname jq lib32gcc-s1 lib32stdc++6 netcat-openbsd pigz python3 rclone tmux unzip util-linux uuid-runtime wget xz-utils libxml2-utils libsdl2-2.0-0:i386 libsdl2-2.0-0 libncurses5 libncurses6 libncursesw5 libncursesw6 tar "

is_lgsm_dependency() {
    [[ "$LGSM_GAME_PACKAGES" == *" $1 "* ]]
}

# Reports whether LinuxGSM scripts are left in any home directory.
game_servers_remain() {
    local f
    for f in /home/*/*server /home/*/*server-*; do
        [ -f "$f" ] && head -c 4096 "$f" 2>/dev/null | grep -q "LinuxGSM" && return 0
    done
    return 1
}

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
        # 32-bit packages exist on amd64 only (arm64 mirrors have no i386 indices).
        [ "$(dpkg --print-architecture 2>/dev/null)" == "amd64" ] && dpkg --add-architecture i386 > /dev/null 2>&1
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

# Architecture, systemd and RAM checks shared by dashboard and agent installations.
# $1 = "true" for an update (no RAM question).
preflight_checks() {
    local is_update=$1
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
}

# Installs the tools, the LinuxGSM game dependencies and the binary (release or build from
# source). Shared by dashboard and agent installations. $1 = release | main | dev | local.
# Sets INSTALL_VERSION and INSTALL_PACKAGES (packages installed by this script so far).
INSTALL_VERSION=""
INSTALL_PACKAGES=""
install_tools_and_binary() {
    local mode=$1
    STATE_CURL_PREEXISTED=$(track_tool curl "$STATE_CURL_PREEXISTED")
    STATE_SUDO_PREEXISTED=$(track_tool sudo "$STATE_SUDO_PREEXISTED")
    # shellcheck disable=SC2206
    local installed_packages=($STATE_INSTALLED_DEPS)
    add_installed() {
        local p
        for p in "${installed_packages[@]}"; do [ "$p" == "$1" ] && return; done
        installed_packages+=("$1")
    }

    command -v curl &> /dev/null || { install_package curl; add_installed curl; }

    # Go is not installed as a package: build_from_source uses a temporary official Go release
    # unless the system already has a new enough Go.
    if [ "$mode" != "release" ]; then
        STATE_GIT_PREEXISTED=$(track_tool git "$STATE_GIT_PREEXISTED")
        if [ "$mode" != "local" ] && ! command -v git &> /dev/null; then
            install_package git
            add_installed git
        fi
    fi

    local new_deps=()
    install_lgsm_dependencies new_deps
    local p
    for p in "${new_deps[@]}"; do add_installed "$p"; done
    INSTALL_PACKAGES="${installed_packages[*]}"

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
    INSTALL_VERSION="$version"
    echo -e "${GREEN}[OK] Dashboard $version installed to $BINARY.${NC}"
}

# Writes a systemd unit with the sandboxing both the dashboard and the agent use (they run the
# same LinuxGSM operations). $1 = service name, $2 = description, $3 = ExecStart.
write_service_unit() {
    cat <<EOT > "/etc/systemd/system/$1.service"
[Unit]
Description=$2
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$3
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
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_DAC_OVERRIDE CAP_KILL CAP_SETUID CAP_SETGID CAP_CHOWN CAP_FOWNER CAP_DAC_READ_SEARCH CAP_NET_BIND_SERVICE CAP_NET_ADMIN CAP_NET_RAW

[Install]
WantedBy=multi-user.target
EOT
}

# A machine is either a dashboard or a node (agent) of another dashboard, never both.
# $1 = the role about to be installed (dashboard | agent).
check_role_conflict() {
    if [ "$1" == "agent" ] && [ -f "/etc/systemd/system/$SERVICE_NAME.service" ]; then
        echo -e "${RED}[ERROR] This machine runs the dashboard. A machine can be either the dashboard or a node of another dashboard; uninstall the dashboard first.${NC}"
        return 1
    fi
    if [ "$1" == "dashboard" ] && [ -f "/etc/systemd/system/$AGENT_SERVICE_NAME.service" ]; then
        echo -e "${RED}[ERROR] This machine is a node of another dashboard (agent). Uninstall the agent first to install the dashboard here.${NC}"
        return 1
    fi
}

# $1 = release | main | dev | local
do_install() {
    local mode="${1:-release}"
    local is_update="false"
    [ -x "$BINARY" ] && is_update="true"

    check_role_conflict dashboard || return 1

    echo -e "\n${BLUE}==================================================================${NC}"
    if [ "$is_update" == "true" ]; then
        echo -e "${MAGENTA}                  Updating installation ($mode)...                ${NC}"
    else
        echo -e "${MAGENTA}                  Starting installation ($mode)...                ${NC}"
    fi
    echo -e "${BLUE}==================================================================${NC}"

    # 1. Pre-flight checks
    preflight_checks "$is_update" || return 1

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

    # 3. Tools, game dependencies and binary
    install_tools_and_binary "$mode" || return 1
    local version="$INSTALL_VERSION"
    write_state "$INSTALL_PACKAGES" "$mode" "$version" dashboard

    # 5. systemd service (with security sandboxing)
    echo -e "${CYAN}[SYS] Writing systemd service...${NC}"
    write_service_unit "$SERVICE_NAME" "LinuxGSM Web Dashboard" "$BINARY -port $PORT -config-dir $INSTALL_DIR/config"

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
# Node (agent of another dashboard)
# ------------------------------------------------------------------

# Values of a pairing command, set by parse_join_command.
JOIN_URL=""
JOIN_TOKEN=""
JOIN_FINGERPRINT=""
JOIN_MODE=""

# Parses the pairing command the dashboard shows under Settings > Nodes, e.g.
#   /opt/lgsm-dashboard/lgsm-dashboard agent join https://dash.example --token ABCD-... \
#     --ca-fingerprint sha256:... --mode read-only
# Only the values are used; the command itself is never executed. Returns 1 if it is invalid.
parse_join_command() {
    local words=() i
    read -r -a words <<< "$1"
    JOIN_URL="" JOIN_TOKEN="" JOIN_FINGERPRINT="" JOIN_MODE=""
    for ((i = 0; i < ${#words[@]}; i++)); do
        case "${words[$i]}" in
            join) JOIN_URL="${words[$((i + 1))]:-}" ;;
            --token) JOIN_TOKEN="${words[$((i + 1))]:-}" ;;
            --ca-fingerprint) JOIN_FINGERPRINT="${words[$((i + 1))]:-}" ;;
            --mode) JOIN_MODE="${words[$((i + 1))]:-}" ;;
        esac
    done
    # Only the URL (with --token and --ca-fingerprint as options of their own).
    [ -z "$JOIN_URL" ] && [[ "${words[0]:-}" =~ ^(https?|tls):// ]] && JOIN_URL="${words[0]}"
    # In a bracket expression "]" must come first to be literal ("[" for IPv6 hosts is plain).
    local url_re='^(https?|tls)://[][A-Za-z0-9._~:/-]+$'
    if ! [[ "$JOIN_URL" =~ $url_re ]]; then
        echo -e "${RED}[ERROR] The pairing command contains no valid dashboard URL.${NC}"
        return 1
    fi
    if ! [[ "$JOIN_TOKEN" =~ ^[A-Za-z2-7-]{16,64}$ ]]; then
        echo -e "${RED}[ERROR] The pairing command contains no valid token.${NC}"
        return 1
    fi
    if ! [[ "$JOIN_FINGERPRINT" =~ ^sha256:[0-9a-f]{64}$ ]]; then
        echo -e "${RED}[ERROR] The pairing command contains no valid CA fingerprint.${NC}"
        return 1
    fi
    if [ -n "$JOIN_MODE" ] && [ "$JOIN_MODE" != "read-only" ] && [ "$JOIN_MODE" != "read-write" ]; then
        echo -e "${RED}[ERROR] The pairing command contains an invalid access mode.${NC}"
        return 1
    fi
    JOIN_MODE="${JOIN_MODE:-read-only}"
}

# Asks for the access mode of this node. The dashboard's choice is the default; this machine
# can only restrict it further. Sets JOIN_MODE.
choose_agent_mode() {
    if [ -n "$AGENT_MODE_ARG" ]; then
        JOIN_MODE="$AGENT_MODE_ARG"
        return
    fi
    has_tty || return
    local default=1 answer=""
    [ "$JOIN_MODE" == "read-write" ] && default=2
    echo -e "${CYAN}Access the dashboard gets on this machine:${NC}"
    echo -e "  1) Read only: view servers, consoles, logs and files; start, stop and restart"
    echo -e "  2) Read and write: everything, including configs, files, backups and installing servers"
    prompt_user "Choose [1-2, default: $default]: " answer
    case "${answer:-$default}" in
        1) JOIN_MODE="read-only" ;;
        2) JOIN_MODE="read-write" ;;
        *) echo -e "${YELLOW}[INFO] Invalid choice, keeping $JOIN_MODE.${NC}" ;;
    esac
}

# Installs or updates this machine as node of another dashboard. $1 = release | main | dev | local.
# A new installation needs the pairing command (--join or interactive); an update keeps the
# existing pairing unless --join is given again.
do_install_agent() {
    local mode="${1:-release}"
    local is_update="false"
    [ -f "/etc/systemd/system/$AGENT_SERVICE_NAME.service" ] && [ -x "$BINARY" ] && is_update="true"
    local config_dir="$INSTALL_DIR/config"

    check_role_conflict agent || return 1

    echo -e "\n${BLUE}==================================================================${NC}"
    if [ "$is_update" == "true" ]; then
        echo -e "${MAGENTA}              Updating node agent ($mode)...                      ${NC}"
    else
        echo -e "${MAGENTA}              Installing node agent ($mode)...                    ${NC}"
    fi
    echo -e "${BLUE}==================================================================${NC}"

    # The pairing command is checked before anything is installed.
    local join_cmd="$AGENT_JOIN_ARG" paired="false"
    [ -f "$config_dir/agent.json" ] && paired="true"
    if [ -z "$join_cmd" ] && [ "$paired" != "true" ]; then
        if ! has_tty; then
            echo -e "${RED}[ERROR] Pass the pairing command from the dashboard (Settings > Nodes) with --join '<command>'.${NC}"
            return 1
        fi
        echo -e "${CYAN}Create a pairing command in the dashboard under Settings > Nodes and paste it here.${NC}"
        prompt_user "Pairing command: " join_cmd
    fi
    if [ -n "$join_cmd" ]; then
        parse_join_command "$join_cmd" || return 1
        choose_agent_mode
    fi

    preflight_checks "$is_update" || return 1
    load_state
    mkdir -p "$INSTALL_DIR"
    install_tools_and_binary "$mode" || return 1
    local version="$INSTALL_VERSION"
    PORT=""
    # FastDL served by the node itself (optional); kept across updates.
    [ -n "$AGENT_FASTDL_PORT_ARG" ] && STATE_FASTDL_PORT="$AGENT_FASTDL_PORT_ARG"
    [ -n "$AGENT_FASTDL_URL_ARG" ] && STATE_FASTDL_URL="$AGENT_FASTDL_URL_ARG"
    [ "$STATE_FASTDL_PORT" == "0" ] && STATE_FASTDL_PORT=""
    write_state "$INSTALL_PACKAGES" "$mode" "$version" agent

    if [ -n "$join_cmd" ]; then
        echo -e "${CYAN}[SYS] Pairing with $JOIN_URL ($JOIN_MODE)...${NC}"
        if ! "$BINARY" agent join "$JOIN_URL" --token "$JOIN_TOKEN" --ca-fingerprint "$JOIN_FINGERPRINT" \
            --mode "$JOIN_MODE" --config-dir "$config_dir"; then
            echo -e "${RED}[ERROR] Pairing failed. Create a new pairing command in the dashboard and run this script again.${NC}"
            return 1
        fi
    fi
    if [ -z "$join_cmd" ] && [ -n "$AGENT_MODE_ARG" ]; then
        # An update without a new pairing: --mode changes the local access mode.
        echo -e "${CYAN}[SYS] Setting the access mode to $AGENT_MODE_ARG...${NC}"
        "$BINARY" agent mode "$AGENT_MODE_ARG" --config-dir "$config_dir" || return 1
    fi

    echo -e "${CYAN}[SYS] Writing systemd service...${NC}"
    local exec_start="$BINARY agent run --config-dir $config_dir"
    [ -n "$STATE_FASTDL_PORT" ] && exec_start+=" --fastdl-port $STATE_FASTDL_PORT"
    [ -n "$STATE_FASTDL_URL" ] && exec_start+=" --fastdl-url $STATE_FASTDL_URL"
    write_service_unit "$AGENT_SERVICE_NAME" "LinuxGSM Dashboard node agent" "$exec_start"
    systemctl daemon-reload
    systemctl enable "$AGENT_SERVICE_NAME" > /dev/null 2>&1
    systemctl restart "$AGENT_SERVICE_NAME"

    # The agent logs "Connected to" once the dashboard has used the connection.
    local i connected="false"
    for i in $(seq 1 15); do
        if journalctl -u "$AGENT_SERVICE_NAME" --since "-30s" --no-pager 2>/dev/null | grep -q "Connected to"; then
            connected="true"
            break
        fi
        systemctl is-active --quiet "$AGENT_SERVICE_NAME" || break
        sleep 1
    done

    echo -e "\n${BLUE}==================================================================${NC}"
    if ! systemctl is-active --quiet "$AGENT_SERVICE_NAME"; then
        echo -e "${RED}[ERROR] The agent did not start. Check: journalctl -u $AGENT_SERVICE_NAME -n 50${NC}"
        echo -e "${BLUE}==================================================================${NC}"
        return 1
    fi
    if [ "$is_update" == "true" ]; then
        echo -e "${GREEN}      Node agent updated to $version${NC}"
    else
        echo -e "${GREEN}      Node agent installed ($version)${NC}"
    fi
    echo -e "${BLUE}==================================================================${NC}"
    "$BINARY" agent status --config-dir "$config_dir" 2>&1 | sed 's/^/  /'
    if [ "$connected" == "true" ]; then
        echo -e "  ${GREEN}Connected to the dashboard.${NC}"
    else
        echo -e "  ${YELLOW}Not connected yet. Check: journalctl -u $AGENT_SERVICE_NAME -n 50${NC}"
    fi
    if [ -n "$STATE_FASTDL_PORT" ]; then
        echo -e "  FastDL of servers set to \"the node itself\" is served on port $STATE_FASTDL_PORT (${STATE_FASTDL_URL:-no --fastdl-url set})."
        echo -e "  Open that port in the firewall if game clients should download from it."
    else
        echo -e "  No port is opened on this machine; the agent connects to the dashboard."
    fi
    echo -e "  Change the access mode later with: $BINARY agent mode read-only|read-write --config-dir $config_dir"
    echo -e "  (then: systemctl restart $AGENT_SERVICE_NAME)"
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

    # 1. Stop and remove the systemd services (dashboard or node agent)
    local is_agent="false"
    if [ -f "/etc/systemd/system/$AGENT_SERVICE_NAME.service" ] || [ "$STATE_ROLE" == "agent" ]; then
        is_agent="true"
    fi
    echo -e "${CYAN}[SYS] Stopping and removing systemd service...${NC}"
    local svc
    for svc in "$SERVICE_NAME" "$AGENT_SERVICE_NAME"; do
        systemctl stop "$svc" > /dev/null 2>&1
        systemctl disable "$svc" > /dev/null 2>&1
        rm -f "/etc/systemd/system/$svc.service"
    done
    systemctl daemon-reload
    # Temporary sudo rules of installs by versions before 2.0.0 that were interrupted.
    rm -f /etc/sudoers.d/lgsm-*

    # 2. Remove firewall rules (a node agent opens no port)
    [ "$is_agent" != "true" ] && configure_firewall "remove"

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
        local pkg keep_game_deps="false"
        # Game servers stay installed: the packages LinuxGSM needs (tmux, 32-bit libraries, ...)
        # must stay too.
        if game_servers_remain; then
            keep_game_deps="true"
            echo -e "${YELLOW}[INFO] Game servers remain on this machine, keeping the packages LinuxGSM needs.${NC}"
        fi
        for pkg in $STATE_INSTALLED_DEPS; do
            # sudo was installed by versions before 2.0.0; it may be in use by now (and apt refuses).
            [ "$pkg" == "sudo" ] && continue
            if [ "$keep_game_deps" == "true" ] && is_lgsm_dependency "$pkg"; then
                continue
            fi
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
    if [ "$is_agent" == "true" ]; then
        echo -e "${YELLOW}[INFO] Remove the node in the dashboard as well (Settings > Nodes).${NC}"
    fi
}

# Status and diagnosis information
show_status() {
    echo -e "\n${BLUE}==================================================================${NC}"
    echo -e "${CYAN}                  Status & System Information                     ${NC}"
    echo -e "${BLUE}==================================================================${NC}"

    local svc label
    for svc in "$SERVICE_NAME" "$AGENT_SERVICE_NAME"; do
        [ "$svc" == "$AGENT_SERVICE_NAME" ] && [ ! -f "/etc/systemd/system/$svc.service" ] && continue
        label="Service Status:   "
        [ "$svc" == "$AGENT_SERVICE_NAME" ] && label="Agent Status:     "
        if systemctl is-active --quiet "$svc"; then
            echo -e "$label ${GREEN}ACTIVE (Running)${NC}"
        else
            echo -e "$label ${RED}INACTIVE (Stopped / Not installed)${NC}"
        fi
    done

    if [ -x "$BINARY" ]; then
        echo -e "Installation Path: ${GREEN}$INSTALL_DIR${NC}"
        echo -e "Version:           ${GREEN}$("$BINARY" -version 2>/dev/null)${NC}"
    else
        echo -e "Installation Path: ${RED}Not installed${NC}"
    fi
    if [ -f "$INSTALL_DIR/config/agent.json" ] && [ -x "$BINARY" ]; then
        echo -e "\n${YELLOW}Node agent:${NC}"
        "$BINARY" agent status --config-dir "$INSTALL_DIR/config" 2>&1 | sed 's/^/  /'
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
ROLE_ARG=""
AGENT_JOIN_ARG=""
AGENT_TOKEN_ARG=""
AGENT_FINGERPRINT_ARG=""
AGENT_MODE_ARG=""
AGENT_FASTDL_PORT_ARG=""
AGENT_FASTDL_URL_ARG=""
INSECURE_SKIP_SIGNATURE="false"

# Options with a value fail instead of looping forever when the value is missing.
need_value() {
    if [ "$2" -lt 2 ]; then
        echo -e "${RED}[ERROR] Option $1 needs a value.${NC}"
        exit 1
    fi
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --insecure-skip-signature)
            INSECURE_SKIP_SIGNATURE="true"
            shift
            ;;
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
        agent|--agent)
            ROLE_ARG="agent"
            shift
            ;;
        --join)
            need_value "$1" $#
            ROLE_ARG="agent"
            AGENT_JOIN_ARG="${2:-}"
            shift 2
            ;;
        # The parts of the pairing command may also be passed as separate options:
        # --join <url> --token <token> --ca-fingerprint <sha256:...>
        --token)
            need_value "$1" $#
            AGENT_TOKEN_ARG="$2"
            shift 2
            ;;
        --ca-fingerprint)
            need_value "$1" $#
            AGENT_FINGERPRINT_ARG="$2"
            shift 2
            ;;
        --fastdl-port)
            need_value "$1" $#
            AGENT_FASTDL_PORT_ARG="${2:-}"
            shift 2
            ;;
        --fastdl-url)
            need_value "$1" $#
            AGENT_FASTDL_URL_ARG="${2:-}"
            shift 2
            ;;
        --mode)
            need_value "$1" $#
            AGENT_MODE_ARG="${2:-}"
            shift 2
            ;;
        --port)
            need_value "$1" $#
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

[ -n "$AGENT_TOKEN_ARG" ] && AGENT_JOIN_ARG+=" --token $AGENT_TOKEN_ARG"
[ -n "$AGENT_FINGERPRINT_ARG" ] && AGENT_JOIN_ARG+=" --ca-fingerprint $AGENT_FINGERPRINT_ARG"

if [ -n "$AGENT_MODE_ARG" ] && [ "$AGENT_MODE_ARG" != "read-only" ] && [ "$AGENT_MODE_ARG" != "read-write" ]; then
    echo -e "${RED}[ERROR] Invalid access mode: $AGENT_MODE_ARG (read-only or read-write).${NC}"
    exit 1
fi
# Both values end up in the systemd unit and in the state file, which is sourced.
if [ -n "$AGENT_FASTDL_PORT_ARG" ] && { ! [[ "$AGENT_FASTDL_PORT_ARG" =~ ^[0-9]+$ ]] || [ "$AGENT_FASTDL_PORT_ARG" -gt 65535 ]; }; then
    echo -e "${RED}[ERROR] Invalid FastDL port: $AGENT_FASTDL_PORT_ARG (0 turns it off).${NC}"
    exit 1
fi
fastdl_url_re='^https?://[][A-Za-z0-9._~:/-]+$'
if [ -n "$AGENT_FASTDL_URL_ARG" ] && ! [[ "$AGENT_FASTDL_URL_ARG" =~ $fastdl_url_re ]]; then
    echo -e "${RED}[ERROR] Invalid FastDL URL: $AGENT_FASTDL_URL_ARG (e.g. http://203.0.113.5:8090/fastdl/).${NC}"
    exit 1
fi

# An installation request on an existing node updates the agent, also without --agent. That
# covers "install", the source build flags and runs without a terminal; interactive runs without
# arguments still show the menu.
if [ -z "$ROLE_ARG" ] && [ -f "/etc/systemd/system/$AGENT_SERVICE_NAME.service" ] &&
   { [ "${ACTION:-}" == "install" ] || [ "$MODE" != "release" ] || { [ -z "$ACTION" ] && ! has_tty; }; }; then
    ROLE_ARG="agent"
fi

# Validate port format
if ! [[ "$PORT" =~ ^[0-9]+$ ]] || [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
    echo -e "${RED}[ERROR] Invalid port: $PORT. Must be a number between 1 and 65535.${NC}"
    exit 1
fi

if [ -n "$ACTION" ] || [ "$MODE" != "release" ] || [ "$ROLE_ARG" == "agent" ]; then
    case ${ACTION:-install} in
        install)
            if [ "$ROLE_ARG" == "agent" ]; then
                do_install_agent "$MODE"
            else
                do_install "$MODE"
            fi
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
            echo -e "Usage: $0 [install|uninstall|status] [--port <port>] [--source|--dev|--local] [--agent [--join '<pairing command>' | --join <url> --token <token> --ca-fingerprint <fp>] [--mode read-only|read-write] [--fastdl-port <port>] [--fastdl-url <url>]]"
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
    echo -e "  4) Install / update as node of another dashboard (agent)"
    echo -e "  5) Completely uninstall dashboard or agent"
    echo -e "  6) Show status & system info"
    echo -e "  7) Exit"
    echo -e "${BLUE}==================================================================${NC}"
    if ! prompt_user "Please choose an option [1-7]: " choice; then
        echo -e "${RED}[ERROR] Input stream closed. Exiting...${NC}"
        exit 1
    fi

    case $choice in
        1) do_install release ;;
        2) do_install main ;;
        3) do_install dev ;;
        4) do_install_agent release ;;
        5) do_uninstall ;;
        6) show_status ;;
        7)
            echo -e "${GREEN}Goodbye!${NC}"
            exit 0
            ;;
        *)
            echo -e "${RED}[ERROR] Invalid option. Please choose 1-7.${NC}"
            ;;
    esac
done
