#!/bin/bash

# LinuxGSM Web Dashboard - Control, Installation, and Uninstallation Script
# Can be run directly via curl:
# curl -sSL https://raw.githubusercontent.com/yourdawi/linuxgsm-dashboard/main/install-dashboard.sh | bash

# Terminal output colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

INSTALL_DIR="/opt/lgsm-dashboard"
STATE_FILE="$INSTALL_DIR/.install-state"
SERVICE_NAME="lgsm-dashboard"

# 1. Check for root privileges
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}[ERROR] This script must be run as root (e.g. using sudo bash).${NC}"
    exit 1
fi

# 2. Detect operating system
OS_TYPE="unknown"
if [ -f /etc/os-release ]; then
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
        read -p "$prompt_msg" "$var_name"
    else
        read -p "$prompt_msg" "$var_name" < /dev/tty
    fi
}

# Helper to check if a package is installed
is_package_installed() {
    local pkg=$1
    if [ "$OS_TYPE" == "debian" ]; then
        dpkg -s "$pkg" &> /dev/null
    elif [ "$OS_TYPE" == "redhat" ]; then
        rpm -q "$pkg" &> /dev/null
    elif [ "$OS_TYPE" == "arch" ]; then
        pacman -Q "$pkg" &> /dev/null
    elif [ "$OS_TYPE" == "alpine" ]; then
        apk info -e "$pkg" &> /dev/null
    elif [ "$OS_TYPE" == "suse" ]; then
        rpm -q "$pkg" &> /dev/null
    else
        return 1
    fi
}

# Helper function to install packages
install_package() {
    local pkg=$1
    echo -e "${YELLOW}[SYS] Installing required package: $pkg...${NC}"
    if [ "$OS_TYPE" == "debian" ]; then
        apt-get update -qq
        apt-get install -y $pkg > /dev/null 2>&1
    elif [ "$OS_TYPE" == "redhat" ]; then
        if command -v dnf &> /dev/null; then
            dnf install -y $pkg > /dev/null 2>&1
        else
            yum install -y $pkg > /dev/null 2>&1
        fi
    elif [ "$OS_TYPE" == "arch" ]; then
        pacman -Sy --noconfirm $pkg > /dev/null 2>&1
    elif [ "$OS_TYPE" == "alpine" ]; then
        apk add --no-cache $pkg > /dev/null 2>&1
    elif [ "$OS_TYPE" == "suse" ]; then
        zypper --non-interactive install $pkg > /dev/null 2>&1
    else
        echo -e "${RED}[WARNING] Unknown system. Please install '$pkg' manually.${NC}"
    fi
}

# Helper function to uninstall packages
uninstall_package() {
    local pkg=$1
    echo -e "${YELLOW}[SYS] Removing package: $pkg...${NC}"
    if [ "$OS_TYPE" == "debian" ]; then
        apt-get purge -y $pkg > /dev/null 2>&1
        apt-get autoremove -y > /dev/null 2>&1
    elif [ "$OS_TYPE" == "redhat" ]; then
        if command -v dnf &> /dev/null; then
            dnf remove -y $pkg > /dev/null 2>&1
        else
            yum remove -y $pkg > /dev/null 2>&1
        fi
    elif [ "$OS_TYPE" == "arch" ]; then
        pacman -Rs --noconfirm $pkg > /dev/null 2>&1
    elif [ "$OS_TYPE" == "alpine" ]; then
        apk del $pkg > /dev/null 2>&1
    elif [ "$OS_TYPE" == "suse" ]; then
        zypper --non-interactive remove $pkg > /dev/null 2>&1
    fi
}

configure_firewall() {
    local action=$1
    if command -v ufw &> /dev/null && ufw status | grep -q "Status: active"; then
        if [ "$action" == "add" ]; then
            echo -e "${CYAN}[SYS] Configuring UFW firewall: Opening TCP port $PORT...${NC}"
            ufw allow $PORT/tcp comment "LinuxGSM Dashboard" > /dev/null
        else
            echo -e "${CYAN}[SYS] Removing UFW firewall rule for TCP port $PORT...${NC}"
            ufw delete allow $PORT/tcp > /dev/null
        fi
    elif command -v firewall-cmd &> /dev/null && systemctl is-active --quiet firewalld; then
        if [ "$action" == "add" ]; then
            echo -e "${CYAN}[SYS] Configuring Firewalld: Opening TCP port $PORT...${NC}"
            firewall-cmd --permanent --add-port=$PORT/tcp > /dev/null
            firewall-cmd --reload > /dev/null
        else
            echo -e "${CYAN}[SYS] Removing Firewalld rule for TCP port $PORT...${NC}"
            firewall-cmd --permanent --remove-port=$PORT/tcp > /dev/null
            firewall-cmd --reload > /dev/null
        fi
    fi
}

# Main installer function
do_install() {
    echo -e "\n${BLUE}==================================================================${NC}"
    echo -e "${MAGENTA}                  Starting Installation...                        ${NC}"
    echo -e "${BLUE}==================================================================${NC}"

    # 1. Pre-flight Checks
    echo -e "${CYAN}[SYS] Running pre-flight system checks...${NC}"
    
    local arch=$(uname -m)
    if [ "$arch" != "x86_64" ] && [ "$arch" != "amd64" ]; then
        echo -e "${RED}[ERROR] Unsupported architecture: $arch. LinuxGSM gameservers require x86_64.${NC}"
        return 1
    fi
    
    if [ -f /proc/meminfo ]; then
        local total_ram=$(awk '/MemTotal/ {print $2}' /proc/meminfo)
        if [ "$total_ram" -lt 2000000 ]; then
            echo -e "${YELLOW}[WARNING] Your system has less than 2GB of RAM. Gameservers might perform poorly or fail to run.${NC}"
            if [ -c /dev/tty ]; then
                read -p "Do you want to continue anyway? [y/N]: " ram_confirm < /dev/tty
                if [[ ! "$ram_confirm" =~ ^[yY]$ ]]; then
                    echo -e "${YELLOW}[INFO] Installation aborted.${NC}"
                    return 1
                fi
            fi
        fi
    fi

    # 2. Port configuration & validation
    if [ -c /dev/tty ] || [ -t 0 ]; then
        prompt_user "Please enter the port for the dashboard [default: $PORT]: " user_port
        if [ -n "$user_port" ]; then
            if ! [[ "$user_port" =~ ^[0-9]+$ ]] || [ "$user_port" -lt 1 ] || [ "$user_port" -gt 65535 ]; then
                echo -e "${RED}[ERROR] Invalid port entered. Using default $PORT.${NC}"
            else
                PORT="$user_port"
            fi
        fi
    fi

    if command -v ss &> /dev/null; then
        if ss -tuln | grep -q ":$PORT "; then
            echo -e "${RED}[ERROR] Port $PORT is already in use on this system. Please choose another port.${NC}"
            return 1
        fi
    elif command -v netstat &> /dev/null; then
        if netstat -tuln | grep -q ":$PORT "; then
            echo -e "${RED}[ERROR] Port $PORT is already in use on this system. Please choose another port.${NC}"
            return 1
        fi
    fi

    # Create installation directory
    mkdir -p "$INSTALL_DIR"

    # Dependency tracking
    local installed_packages=()
    local git_preexisted="false"
    local go_preexisted="false"
    local curl_preexisted="false"
    local sudo_preexisted="false"

    if is_package_installed "git"; then
        git_preexisted="true"
        echo -e "${GREEN}[INFO] Git was already present on the system.${NC}"
    else
        install_package "git"
        installed_packages+=("git")
    fi

    if is_package_installed "curl"; then
        curl_preexisted="true"
        echo -e "${GREEN}[INFO] curl was already present on the system.${NC}"
    else
        install_package "curl"
        installed_packages+=("curl")
    fi

    local go_pkg="golang"
    if [ "$OS_TYPE" == "arch" ] || [ "$OS_TYPE" == "alpine" ] || [ "$OS_TYPE" == "suse" ]; then
        go_pkg="go"
    fi

    if is_package_installed "$go_pkg" || command -v go &> /dev/null; then
        go_preexisted="true"
        echo -e "${GREEN}[INFO] Go was already present on the system.${NC}"
    else
        install_package "$go_pkg"
        installed_packages+=("$go_pkg")
    fi

    if is_package_installed "sudo"; then
        sudo_preexisted="true"
    else
        echo -e "${YELLOW}[SYS] sudo is missing. Installing sudo...${NC}"
        install_package "sudo"
        installed_packages+=("sudo")
    fi

    # Pre-install LinuxGSM game server dependencies
    echo -e "${YELLOW}[SYS] Checking and installing LinuxGSM game server dependencies...${NC}"

    try_install() {
        local primary="$1"
        local fallback="$2"
        if [ "$OS_TYPE" == "debian" ]; then
            if apt-cache show "$primary" > /dev/null 2>&1; then
                apt-get install -y "$primary" > /dev/null 2>&1
                return
            elif [ -n "$fallback" ] && apt-cache show "$fallback" > /dev/null 2>&1; then
                apt-get install -y "$fallback" > /dev/null 2>&1
                return
            fi
        elif [ "$OS_TYPE" == "redhat" ]; then
            if command -v dnf &> /dev/null; then
                dnf install -y "$primary" > /dev/null 2>&1
            else
                yum install -y "$primary" > /dev/null 2>&1
            fi
        fi
    }

    if [ "$OS_TYPE" == "debian" ]; then
        dpkg --add-architecture i386 > /dev/null 2>&1
        apt-get update -y > /dev/null 2>&1

        LGSM_DEPS=(
            bc binutils bsdmainutils bzip2 ca-certificates cpio curl
            distro-info file gzip hostname jq lib32gcc-s1 lib32stdc++6
            netcat-openbsd pigz python3 tmux unzip util-linux uuid-runtime
            wget xz-utils libxml2-utils
        )
        for pkg in "${LGSM_DEPS[@]}"; do
            if is_package_installed "$pkg"; then
                echo -e "${GREEN}[INFO] Dependency '$pkg' is already installed.${NC}"
            else
                try_install "$pkg"
                if is_package_installed "$pkg"; then
                    installed_packages+=("$pkg")
                fi
            fi
        done

        # Fallback libraries
        check_and_install_fallback() {
            local primary="$1"
            local fallback="$2"
            if is_package_installed "$primary" || is_package_installed "$fallback"; then
                echo -e "${GREEN}[INFO] Dependency '$primary' or '$fallback' is already installed.${NC}"
                return
            fi
            try_install "$primary" "$fallback"
            if is_package_installed "$primary"; then
                installed_packages+=("$primary")
            elif is_package_installed "$fallback"; then
                installed_packages+=("$fallback")
            fi
        }

        check_and_install_fallback "libsdl2-2.0-0:i386" "libsdl2-2.0-0"
        check_and_install_fallback "libncurses5" "libncurses6"
        check_and_install_fallback "libncursesw5" "libncursesw6"

    elif [ "$OS_TYPE" == "redhat" ]; then
        LGSM_DEPS=(bc binutils bzip2 curl file gzip jq tmux unzip wget tar)
        for pkg in "${LGSM_DEPS[@]}"; do
            if is_package_installed "$pkg"; then
                echo -e "${GREEN}[INFO] Dependency '$pkg' is already installed.${NC}"
            else
                try_install "$pkg"
                if is_package_installed "$pkg"; then
                    installed_packages+=("$pkg")
                fi
            fi
        done
    fi

    echo -e "${GREEN}[OK] LinuxGSM dependencies verified/installed.${NC}"

    if ! command -v go &> /dev/null; then
        echo -e "${RED}[ERROR] Go could not be installed. Aborting.${NC}"
        return 1
    fi

    # Record installation state
    local installed_deps_str="${installed_packages[*]}"
    cat <<EOT > "$STATE_FILE"
GIT_PREEXISTED=$git_preexisted
GO_PREEXISTED=$go_preexisted
CURL_PREEXISTED=$curl_preexisted
SUDO_PREEXISTED=$sudo_preexisted
PORT=$PORT
INSTALLED_DEPS="$installed_deps_str"
INSTALL_DATE="$(date '+%Y-%m-%d %H:%M:%S')"
EOT
    chmod 600 "$STATE_FILE"
    echo -e "${GREEN}[OK] Package installation status logged to: $STATE_FILE${NC}"

    # Determine branch to clone
    local branch_name="main"
    if [ "$DEV_MODE" == "true" ]; then
        branch_name="dev"
    fi

    # Use local development files if present, otherwise clone repository
    if [ -f "./main.go" ] && [ -d "./backend" ] && [ -d "./ui" ] && [ "$branch_name" != "dev" ]; then
        echo -e "${GREEN}[INFO] Copying local development files to $INSTALL_DIR...${NC}"
        cp -r ./main.go ./go.mod ./backend ./ui "$INSTALL_DIR/"
    else
        echo -e "${YELLOW}[SYS] Downloading dashboard repository (branch: $branch_name)...${NC}"
        local repo_url="https://github.com/yourdawi/linuxgsm-dashboard.git"
        local temp_dir=$(mktemp -d)
        
        git clone -b "$branch_name" "$repo_url" "$temp_dir" > /dev/null 2>&1
        if [ $? -eq 0 ]; then
            cp -r "$temp_dir"/* "$INSTALL_DIR/"
            [ -d "$temp_dir/.git" ] && cp -r "$temp_dir/.git" "$INSTALL_DIR/"
            rm -rf "$temp_dir"
        else
            echo -e "${RED}[ERROR] Download failed.${NC}"
            return 1
        fi
    fi

    cd "$INSTALL_DIR" || return 1

    echo -e "${YELLOW}[SYS] Compiling Go code...${NC}"
    go build -o lgsm-dashboard main.go
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}[OK] Dashboard compiled successfully.${NC}"
    else
        echo -e "${RED}[ERROR] Compilation failed.${NC}"
        return 1
    fi

    # Create systemd service file (with security sandboxing)
    echo -e "${CYAN}[SYS] Creating Systemd service file (with security sandboxing)...${NC}"
    cat <<EOT > /etc/systemd/system/$SERVICE_NAME.service
[Unit]
Description=LinuxGSM Web Dashboard
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/lgsm-dashboard -port $PORT -config-dir $INSTALL_DIR/config
Restart=always
RestartSec=5

# Security Sandboxing
ProtectSystem=true
ProtectHome=false
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_DAC_OVERRIDE CAP_KILL CAP_SETUID CAP_SETGID CAP_CHOWN CAP_FOWNER CAP_DAC_READ_SEARCH CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOT

    # Firewall configuration
    local open_fw="false"
    if command -v ufw &> /dev/null && ufw status | grep -q "Status: active"; then
        open_fw="true"
    elif command -v firewall-cmd &> /dev/null && systemctl is-active --quiet firewalld; then
        open_fw="true"
    fi
    
    if [ "$open_fw" == "true" ]; then
        local fw_confirm="y"
        if [ -c /dev/tty ] || [ -t 0 ]; then
            prompt_user "Active firewall detected. Do you want to automatically open port $PORT? [Y/n]: " fw_confirm
        fi
        if [[ "$fw_confirm" =~ ^[yY]?$ ]]; then
            configure_firewall "add"
        fi
    fi

    # Start systemd service
    echo -e "${CYAN}[SYS] Starting Systemd service...${NC}"
    systemctl daemon-reload
    systemctl enable $SERVICE_NAME > /dev/null 2>&1
    systemctl start $SERVICE_NAME

    echo -e "${YELLOW}[SYS] Waiting for service initialization...${NC}"
    sleep 3

    # Extract password from journal
    local password=""
    password=$(journalctl -u $SERVICE_NAME -n 100 --no-pager | grep -oP "Password: \K\w+" | tail -n 1)

    # Get server IP
    local server_ip
    server_ip=$(hostname -I | awk '{print $1}')
    [ -z "$server_ip" ] && server_ip="YOUR-SERVER-IP"

    # Get server public IP
    local public_ip=""
    public_ip=$(curl -s --max-time 2 https://api.ipify.org || wget -qO- --timeout=2 https://api.ipify.org || echo "")

    echo -e "\n${BLUE}==================================================================${NC}"
    echo -e "${GREEN}      LinuxGSM Web Dashboard successfully installed!              ${NC}"
    echo -e "${BLUE}==================================================================${NC}"
    if [ -n "$public_ip" ] && [ "$public_ip" != "$server_ip" ]; then
        echo -e "  URL (Local):  ${CYAN}http://$server_ip:$PORT${NC}"
        echo -e "  URL (Public): ${CYAN}http://$public_ip:$PORT${NC}"
    else
        echo -e "  URL:          ${CYAN}http://$server_ip:$PORT${NC}"
    fi
    echo -e "  Username:  ${CYAN}admin${NC}"
    if [ -n "$password" ]; then
        echo -e "  Password:  ${GREEN}$password${NC}  <-- Please change immediately!"
    else
        echo -e "  Password:  ${RED}Could not read. Run 'journalctl -u $SERVICE_NAME' to view.${NC}"
    fi
    echo -e "${BLUE}==================================================================${NC}"
}

# Uninstaller function
do_uninstall() {
    echo -e "\n${RED}==================================================================${NC}"
    echo -e "${RED}             Starting Uninstallation...                           ${NC}"
    echo -e "${RED}==================================================================${NC}"

    if ! prompt_user "Are you sure you want to delete the dashboard and all configurations? [y/N]: " confirm; then
        echo -e "${RED}[ERROR] Input stream closed. Exiting...${NC}"
        exit 1
    fi
    if [[ ! "$confirm" =~ ^[yY]$ ]]; then
        echo -e "${YELLOW}[INFO] Uninstallation cancelled.${NC}"
        return
    fi

    # Read state file to find configurations
    local git_preexisted="true"
    local go_preexisted="true"
    local curl_preexisted="true"
    local sudo_preexisted="true"
    local PORT="8080"
    local INSTALLED_DEPS=""

    if [ -f "$STATE_FILE" ]; then
        source "$STATE_FILE"
        echo -e "${GREEN}[INFO] Installation log loaded.${NC}"
    else
        echo -e "${YELLOW}[WARNING] No .install-state log file found.${NC}"
    fi

    # 1. Stop and remove systemd service
    echo -e "${CYAN}[SYS] Stopping and removing Systemd service...${NC}"
    systemctl stop $SERVICE_NAME > /dev/null 2>&1
    systemctl disable $SERVICE_NAME > /dev/null 2>&1
    rm -f /etc/systemd/system/$SERVICE_NAME.service
    systemctl daemon-reload

    # 2. Remove firewall rules
    configure_firewall "remove"

    # 3. Clean up system packages if requested
    local remove_packages="false"
    if [ -f "$STATE_FILE" ]; then
        if prompt_user "Do you want to uninstall system packages that were installed by this script? [y/N]: " rm_pkg; then
            if [[ "$rm_pkg" =~ ^[yY]$ ]]; then
                remove_packages="true"
            fi
        fi
    else
        echo -e "${YELLOW}[WARNING] System packages will be kept to prevent issues since no log was found.${NC}"
    fi

    if [ "$remove_packages" == "true" ]; then
        if [ "$git_preexisted" == "false" ]; then
            uninstall_package "git"
        fi
        if [ "$curl_preexisted" == "false" ]; then
            uninstall_package "curl"
        fi
        
        local go_pkg="golang"
        if [ "$OS_TYPE" == "arch" ] || [ "$OS_TYPE" == "alpine" ] || [ "$OS_TYPE" == "suse" ]; then
            go_pkg="go"
        fi
        if [ "$go_preexisted" == "false" ]; then
            uninstall_package "$go_pkg"
        fi
        if [ "$sudo_preexisted" == "false" ]; then
            uninstall_package "sudo"
        fi

        if [ -n "$INSTALLED_DEPS" ]; then
            for pkg in $INSTALLED_DEPS; do
                if [ "$pkg" != "git" ] && [ "$pkg" != "curl" ] && [ "$pkg" != "$go_pkg" ] && [ "$pkg" != "sudo" ]; then
                    uninstall_package "$pkg"
                fi
            done
        fi
    else
        echo -e "${GREEN}[INFO] Keeping all system packages as requested.${NC}"
    fi

    # 4. Delete installation directory
    if [ -d "$INSTALL_DIR" ]; then
        echo -e "${CYAN}[SYS] Deleting installation directory: $INSTALL_DIR...${NC}"
        rm -rf "$INSTALL_DIR"
    fi

    echo -e "\n${GREEN}[OK] LinuxGSM Web Dashboard has been completely removed.${NC}"
}

# Status and diagnosis information
show_status() {
    echo -e "\n${BLUE}==================================================================${NC}"
    echo -e "${CYAN}                  Status & System Information                     ${NC}"
    echo -e "${BLUE}==================================================================${NC}"

    # Check systemd service state
    if systemctl is-active --quiet $SERVICE_NAME; then
        echo -e "Service Status:    ${GREEN}ACTIVE (Running)${NC}"
    else
        echo -e "Service Status:    ${RED}INACTIVE (Stopped / Not installed)${NC}"
    fi

    # Check compilation state / directory
    if [ -f "$INSTALL_DIR/lgsm-dashboard" ]; then
        echo -e "Installation Path: ${GREEN}$INSTALL_DIR${NC}"
    else
        echo -e "Installation Path: ${RED}Not installed${NC}"
    fi

    # System resource summary
    echo -e "\n${YELLOW}System Load:${NC}"
    uptime | awk '{print "  Load Average:   " $8 $9 $10}'
    free -h | awk '/Mem:/ {print "  Memory Usage:   " $3 "/" $2}'
    df -h / | awk '/\// {print "  Disk Usage:     " $3 "/" $2 " used"}'

    # Logged variables
    if [ -f "$STATE_FILE" ]; then
        echo -e "\n${YELLOW}Installation Log:${NC}"
        cat "$STATE_FILE" | sed 's/^/  /'
    fi
    echo -e "${BLUE}==================================================================${NC}"
}

# Parse command line arguments
ACTION=""
PORT="8080"
DEV_MODE="false"

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
            DEV_MODE="true"
            shift
            ;;
        --port)
            PORT="$2"
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

if [ -n "$ACTION" ]; then
    case $ACTION in
        install)
            do_install
            exit 0
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
            echo -e "Usage: $0 [install|uninstall|status] [--port <port>] [--dev]"
            exit 1
            ;;
    esac
fi

# Check if a controlling terminal (TTY) is available
if [ ! -c /dev/tty ]; then
    echo -e "${YELLOW}[INFO] Non-interactive shell detected (no /dev/tty). Running installation directly...${NC}"
    do_install
    exit 0
fi

# Menu loop (interactive only)
while true; do
    echo -e "\n${BLUE}==================================================================${NC}"
    echo -e "${CYAN}             LinuxGSM Web Dashboard - Management Menu             ${NC}"
    echo -e "${BLUE}==================================================================${NC}"
    echo -e "  1) Install / Update Web Dashboard"
    echo -e "  2) Completely Uninstall Dashboard"
    echo -e "  3) Show Status & System Info"
    echo -e "  4) Exit"
    echo -e "${BLUE}==================================================================${NC}"
    if ! prompt_user "Please choose an option [1-4]: " choice; then
        echo -e "${RED}[ERROR] Input stream closed. Exiting...${NC}"
        exit 1
    fi

    case $choice in
        1)
            do_install
            ;;
        2)
            do_uninstall
            ;;
        3)
            show_status
            ;;
        4)
            echo -e "${GREEN}Goodbye!${NC}"
            exit 0
            ;;
        *)
            echo -e "${RED}[ERROR] Invalid option. Please choose 1, 2, 3, or 4.${NC}"
            ;;
    esac
done
