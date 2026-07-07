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

# Main installer function
do_install() {
    echo -e "\n${BLUE}==================================================================${NC}"
    echo -e "${MAGENTA}                  Starting Installation...                        ${NC}"
    echo -e "${BLUE}==================================================================${NC}"

    # Create installation directory
    mkdir -p "$INSTALL_DIR"

    # Check Git and Go installation state
    local git_preexisted="false"
    local go_preexisted="false"

    if command -v git &> /dev/null; then
        git_preexisted="true"
        echo -e "${GREEN}[INFO] Git was already present on the system.${NC}"
    else
        install_package "git"
    fi

    if command -v go &> /dev/null; then
        go_preexisted="true"
        echo -e "${GREEN}[INFO] Go was already present on the system.${NC}"
    else
        local go_pkg="golang"
        if [ "$OS_TYPE" == "arch" ] || [ "$OS_TYPE" == "alpine" ] || [ "$OS_TYPE" == "suse" ]; then
            go_pkg="go"
        fi
        install_package "$go_pkg"
    fi

    # Ensure sudo is installed
    if ! command -v sudo &> /dev/null; then
        echo -e "${YELLOW}[SYS] sudo is missing. Installing sudo...${NC}"
        install_package "sudo"
    fi

    # Pre-install LinuxGSM game server dependencies (once for all future game installs)
    echo -e "${YELLOW}[SYS] Installing LinuxGSM game server dependencies...${NC}"

    # Helper: try to install a package, with optional fallback name
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
        # Enable i386 architecture for 32-bit game server libraries
        dpkg --add-architecture i386 > /dev/null 2>&1
        apt-get update -y > /dev/null 2>&1

        LGSM_DEPS=(
            bc binutils bsdmainutils bzip2 ca-certificates cpio curl
            distro-info file gzip hostname jq lib32gcc-s1 lib32stdc++6
            netcat-openbsd pigz python3 tmux unzip util-linux uuid-runtime
            wget xz-utils libxml2-utils
        )
        for pkg in "${LGSM_DEPS[@]}"; do
            try_install "$pkg"
        done

        # Packages renamed across Debian versions (primary → fallback)
        try_install "libsdl2-2.0-0:i386" "libsdl2-2.0-0"
        try_install "libncurses5" "libncurses6"
        try_install "libncursesw5" "libncursesw6"
    elif [ "$OS_TYPE" == "redhat" ]; then
        LGSM_DEPS=(bc binutils bzip2 curl file gzip jq tmux unzip wget tar)
        for pkg in "${LGSM_DEPS[@]}"; do
            try_install "$pkg"
        done
    fi

    echo -e "${GREEN}[OK] LinuxGSM dependencies installed.${NC}"

    # Verify Go is available now
    if ! command -v go &> /dev/null; then
        echo -e "${RED}[ERROR] Go could not be installed. Aborting.${NC}"
        return 1
    fi

    # Record installation state
    cat <<EOT > "$STATE_FILE"
GIT_PREEXISTED=$git_preexisted
GO_PREEXISTED=$go_preexisted
INSTALL_DATE="$(date '+%Y-%m-%d %H:%M:%S')"
EOT
    chmod 600 "$STATE_FILE"
    echo -e "${GREEN}[OK] Package installation status logged to: $STATE_FILE${NC}"

    # Use local development files if present, otherwise clone repository
    if [ -f "./main.go" ] && [ -d "./backend" ] && [ -d "./ui" ]; then
        echo -e "${GREEN}[INFO] Copying local development files to $INSTALL_DIR...${NC}"
        cp -r ./main.go ./go.mod ./backend ./ui "$INSTALL_DIR/"
    else
        echo -e "${YELLOW}[SYS] Downloading dashboard repository...${NC}"
        local repo_url="https://github.com/yourdawi/linuxgsm-dashboard.git"
        local temp_dir=$(mktemp -d)
        
        git clone "$repo_url" "$temp_dir" > /dev/null 2>&1
        if [ $? -eq 0 ]; then
            cp -r "$temp_dir"/* "$INSTALL_DIR/"
            rm -rf "$temp_dir"
        else
            echo -e "${RED}[ERROR] Download failed.${NC}"
            return 1
        fi
    fi

    cd "$INSTALL_DIR" || return 1

    # Compile binary
    echo -e "${CYAN}[SYS] Compiling Go code...${NC}"
    go build -o lgsm-dashboard main.go
    if [ $? -ne 0 ]; then
        echo -e "${RED}[ERROR] Compilation failed!${NC}"
        return 1
    fi
    echo -e "${GREEN}[OK] Dashboard compiled successfully.${NC}"

    # Create systemd service file
    echo -e "${CYAN}[SYS] Creating Systemd service file...${NC}"
    cat <<EOT > /etc/systemd/system/$SERVICE_NAME.service
[Unit]
Description=LinuxGSM Web Dashboard
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/lgsm-dashboard -port 8080 -config-dir $INSTALL_DIR/config
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOT

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
        echo -e "  URL (Local):  ${CYAN}http://$server_ip:8080${NC}"
        echo -e "  URL (Public): ${CYAN}http://$public_ip:8080${NC}"
    else
        echo -e "  URL:          ${CYAN}http://$server_ip:8080${NC}"
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

    if ! read -p "Are you sure you want to delete the dashboard and all configurations? [y/N]: " confirm < /dev/tty; then
        echo -e "${RED}[ERROR] Input stream closed. Exiting...${NC}"
        exit 1
    fi
    if [[ ! "$confirm" =~ ^[yY]$ ]]; then
        echo -e "${YELLOW}[INFO] Uninstallation cancelled.${NC}"
        return
    fi

    # 1. Stop and remove systemd service
    echo -e "${CYAN}[SYS] Stopping and removing Systemd service...${NC}"
    systemctl stop $SERVICE_NAME > /dev/null 2>&1
    systemctl disable $SERVICE_NAME > /dev/null 2>&1
    rm -f /etc/systemd/system/$SERVICE_NAME.service
    systemctl daemon-reload

    # 2. Read state file and uninstall packages if they were installed by this script
    local git_preexisted="true"
    local go_preexisted="true"

    if [ -f "$STATE_FILE" ]; then
        source "$STATE_FILE"
        echo -e "${GREEN}[INFO] Installation log loaded.${NC}"
    else
        echo -e "${YELLOW}[WARNING] No .install-state log file found. System packages will be kept to prevent issues.${NC}"
    fi

    if [ "$git_preexisted" == "false" ]; then
        uninstall_package "git"
    else
        echo -e "${GREEN}[INFO] Keeping package 'git' as it was already present before installation.${NC}"
    fi

    local go_pkg="golang"
    if [ "$OS_TYPE" == "arch" ] || [ "$OS_TYPE" == "alpine" ] || [ "$OS_TYPE" == "suse" ]; then
        go_pkg="go"
    fi

    if [ "$go_preexisted" == "false" ]; then
        uninstall_package "$go_pkg"
    else
        echo -e "${GREEN}[INFO] Keeping package '$go_pkg' as it was already present before installation.${NC}"
    fi

    # 3. Delete installation directory
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

# Check arguments or interactive mode
ACTION=$1

if [ -n "$ACTION" ]; then
    case $ACTION in
        install|--install)
            do_install
            exit 0
            ;;
        uninstall|--uninstall)
            do_uninstall
            exit 0
            ;;
        status|--status)
            show_status
            exit 0
            ;;
        *)
            echo -e "${RED}[ERROR] Unknown argument: $ACTION${NC}"
            echo -e "Usage: $0 [install|uninstall|status]"
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
    if ! read -p "Please choose an option [1-4]: " choice < /dev/tty; then
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
