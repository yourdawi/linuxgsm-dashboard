# LinuxGSM Web Dashboard

A web interface for managing game servers that run under [LinuxGSM](https://linuxgsm.com/). The dashboard is a single Go binary with the web interface embedded. It runs either natively as a systemd service and controls the LinuxGSM scripts of each game server through the server's own Linux user, or as a Docker container that runs every game server in its own container of the official LinuxGSM images.

<img width="80%" alt="dashboard" src="https://github.com/user-attachments/assets/9d55ae4f-f335-4c0c-89f3-0bcfd48c95c1" />

<details>
  <summary>More screenshots</summary>
  <br>

  **Server console with command input**
  <p align="center">
    <img width="80%" alt="liveconsole" src="https://github.com/user-attachments/assets/a8458425-e7cc-42bc-b0bb-5781636c68e2" />
  </p>

  **Config editor (form and raw view)**
  <p align="center">
    <img width="80%" alt="config" src="https://github.com/user-attachments/assets/9b748768-1c05-40d8-beb6-59ce03b0f217" />
  </p>
  
  **File manager**
  <p align="center">
    <img width="80%" alt="filemanager" src="https://github.com/user-attachments/assets/6613dc2e-fc93-47f9-82a8-93ba076ed822" />
  </p>

  **Game installer**
  <p align="center">
    <img width="80%" alt="gameinstaller" src="https://github.com/user-attachments/assets/1251b841-984f-4c98-b2de-a847bce4bd1a" />
  </p>

  **Overview and monitoring**
  <p align="center">
    <img width="80%" alt="overview" src="https://github.com/user-attachments/assets/dc0e6965-7011-4528-8de3-ba4cff2fd240" />
  </p>
</details>

## Contents

- [Features](#features)
- [Requirements](#requirements)
- [Installation](#installation)
- [Host mode and Docker mode](#host-mode-and-docker-mode)
- [Docker mode in detail](#docker-mode-in-detail)
- [Configuration](#configuration)
- [Game servers outside of /home](#game-servers-outside-of-home)
- [Users and permissions](#users-and-permissions)
- [Two-factor authentication](#two-factor-authentication)
- [Security](#security)
- [Updating](#updating)
- [Known limitations](#known-limitations)
- [Development](#development)
- [Disclaimer](#disclaimer)

## Features

### Server management

- Automatic discovery of LinuxGSM instances (see [where servers are found](#game-servers-outside-of-home)).
- Start, stop, restart, update, validate, backup and `details` for each server, with the command output streamed live into the browser.
- Bulk actions: select several servers and start, stop, restart or update them together.
- Tags for grouping servers (for example "Production" or "Cluster A") and a tag filter on the dashboard.
- Game installer: list of all LinuxGSM games (synchronised from linuxgsm.com), creates the Linux user, bootstraps LinuxGSM and runs `auto-install`. An installation directory outside of `/home` can be chosen.
- Adjustable number of card columns on the dashboard; a fixed column count uses the full window width on large screens.
- Multi-instance cloning: creates an additional instance (for example `vhserver-2`) as an independent copy of the script, including its instance config.
- Port check: probes the configured game, query and RCON ports from the public IP.
- Live status from the game server itself: map, player count, bots and a player list with playtime and score (kick and ban for users with the `console` permission). The dashboard queries running servers every 15 seconds via the Valve/Steam query protocol (A2S, used by most Steam games), the Minecraft Java server list ping and the Minecraft Bedrock ping. Games without a query protocol (for example TeamSpeak 3) simply show no player data.
- Disk usage per server: allocated size of the server directory including backups, plus free space on the disk the server lives on. `serverfiles` and backup directories moved to another disk are included. The size is measured in the background every 10 minutes.
- Server deletion, including its systemd service and cron entries. If the Linux user only hosts this one server, the user and its home directory are removed as well.

### Console and logs

- Live view of the LinuxGSM tmux session with an input field for console commands.
- Four log views: tmux pane, console log, script log (`log/script/`) and game log (`log/game/` or common log files in `serverfiles/`).
- Additional LinuxGSM commands in the console view: `check-update`, `update-lgsm`, `force-update`, `mods-install`, `mods-update`, `fastdl`, `postdetails`, `test-alert` and `debug`.

### Game-specific actions

Shown in the console view depending on the game:

| Game | Actions |
|------|---------|
| Rust | Map wipe, full wipe, map compressor, Oxide/uMod install and update |
| Source engine games (CS2, CS:GO, TF2, CS:S, Garry's Mod, L4D2 and others) | FastDL generation, copyable FastDL URL, Metamod/SourceMod install and update |
| TeamSpeak 3 | Reset the ServerQuery admin password |
| Multi Theft Auto | Install the default resources |
| Unreal Tournament (99, 2004, 3) | Map compressor for redirect downloads |
| Arma 3, Minecraft, 7 Days to Die, Factorio, ARK, Killing Floor 2, Don't Starve Together, Insurgency: Sandstorm, Valheim, TrackMania | Install and update mods/plugins through LinuxGSM, list of installed mods |

The dashboard also serves FastDL content directly under `/fastdl/<server>/`, so the URL can be used for `sv_downloadurl` without a separate web server.

### Configuration and files

- Config editor for the LinuxGSM configuration layers (`_default.cfg`, `common.cfg`, instance config and secrets files) and for config files found in `serverfiles/` (`.cfg`, `.ini`, `.properties`, `.conf`, `.json`). Each file can be edited in a form view or as raw text. LinuxGSM restores `_default.cfg` every time the server script runs, so values changed there are saved to the instance config (`<script>.cfg`), which overrides them.
- Stop mode selection (all 13 LinuxGSM stop modes or the default).
- File manager for `serverfiles/`: browse, view, edit, upload and download files.

### Backups

- List, create, download, upload, delete and restore backups. The backup directory configured in LinuxGSM (`backupdir`) is taken into account.
- All archive formats LinuxGSM creates are supported: `.tar.zst` (used when `zstd` is installed), `.tar.gz` and plain `.tar`. Restoring a `.tar.zst` backup needs `zstd` and GNU tar 1.31 or newer on the host.
- Restores stop a running server first, extract the archive and start the server again. The output is streamed live.
- Uploaded archives are handed over to the server's Linux user, so backups can be moved between hosts.
- Retention settings (`maxbackups`, `maxbackupdays`, `stoponbackup`) and an optional cron schedule for automatic backups.
- Synchronisation of the backup directory to any rclone remote (Google Drive, S3, Backblaze B2, WebDAV and so on). The rclone remote is configured with `rclone config` as the server's Linux user.

### Scheduling and automation

- Scheduler for the server user's crontab: crash monitoring every 5, 10 or 15 minutes, update checks, a daily `force-update` at a chosen time and a weekly `update-lgsm`.
- One-click installation of the recommended LinuxGSM cron jobs and of a systemd service that starts the server at boot. Both are also shown as templates for manual setup.

### Notifications

Configuration of the LinuxGSM alert channels Discord, Telegram, e-mail (SMTP), Matrix, ntfy, Slack, Pushover, Pushbullet, IFTTT and Rocket.Chat. Stored secrets such as webhook URLs and tokens are never sent back to the browser; they are shown masked and stay unchanged unless a new value is entered.

### Users, monitoring and interface

- Multiple dashboard users with roles and per-server permissions (see [Users and permissions](#users-and-permissions)).
- Optional two-factor authentication per user with an authenticator app (see [Two-factor authentication](#two-factor-authentication)).
- Audit log of logins, failed logins, server actions, file and configuration changes and administrative changes (last 1000 entries).
- Host monitoring: CPU, RAM and disk usage of `/` with a history chart, plus players, CPU, RAM, disk usage and process IDs per server.
- Firewall status for UFW and firewalld, including the active rules. Opening a port is available to administrators through the API (`POST /api/system/firewall/open`).
- Update notice in the sidebar for administrators and one-click updates to signed releases (see [Updating](#updating)).
- Light and dark theme, English and German interface.

## Requirements

**Native installation (installer):**

- A 64-bit Linux host with systemd (x86_64; the dashboard also runs on ARM64, but most LinuxGSM game servers need x86_64). The installer supports Debian/Ubuntu, RHEL/CentOS/AlmaLinux/Rocky, Arch and openSUSE; LinuxGSM game dependencies are installed automatically on Debian/Ubuntu and RHEL-based systems, on the others LinuxGSM lists missing packages during the game server installation. Signature checks in the installer need OpenSSL 3 (older systems fall back to the checksum).
- Root privileges. The dashboard creates Linux users and runs commands as the game server users via `runuser`.
- Only for builds from source: Git and Go 1.20 or newer. Go 1.18 and 1.19 can build the project as well, but without the per-request timeouts described under [Security](#security).

**Docker:**

- A 64-bit Linux host with Docker Engine (and the Compose plugin for `docker compose`). The official game server images are built for x86_64.
- Root or membership in the `docker` group to start the container. The dashboard needs the Docker socket.

## Installation

There are two ways to run the dashboard: natively on a Linux host with the installer, or as a Docker container that runs every game server in its own container. Both use the same interface; [Host mode and Docker mode](#host-mode-and-docker-mode) lists the differences.

### Native installation with the installer

Run the installer as root (for example after `sudo -i`):

```bash
curl -sSL https://raw.githubusercontent.com/yourdawi/linuxgsm-dashboard/main/install-dashboard.sh | bash
```

If `curl` is not available:

```bash
wget -qO- https://raw.githubusercontent.com/yourdawi/linuxgsm-dashboard/main/install-dashboard.sh | bash
```

The installer opens a menu:

| Option | What it does |
|--------|--------------|
| 1. Install / update (latest release) | Downloads the latest release binary, checks its signature (OpenSSL 3) and checksum, and installs it. No Go or Git needed. |
| 2. Install / update from source: main branch | Clones `main` and compiles it. The result is a development build (`dev-main-<commit>`). |
| 3. Install / update from source: dev branch | Same with the `dev` branch (`dev-dev-<commit>`). |
| 4. Uninstall | Removes the service and `/opt/lgsm-dashboard`. Packages are only removed if the installer installed them. Game server users and their files are kept. |
| 5. Status | Service state, installed version and installation log. |

The same can be done without the menu:

```bash
bash install-dashboard.sh install              # latest release
bash install-dashboard.sh install --source     # build from main
bash install-dashboard.sh install --dev        # build from dev
bash install-dashboard.sh install --local      # build from the current directory (repository checkout)
bash install-dashboard.sh install --port 9000  # choose the port
bash install-dashboard.sh status
bash install-dashboard.sh uninstall
```

During an installation the installer:

1. checks the architecture and warns if the host has less than 2 GB of RAM,
2. asks for the port on a new installation (an update keeps the current port) and checks that it is free,
3. installs missing tools (curl and sudo; Git and Go only for builds from source) and the LinuxGSM game dependencies, and records which packages it installed,
4. installs the binary to `/opt/lgsm-dashboard/lgsm-dashboard` and keeps the previous one as `lgsm-dashboard.old`,
5. writes the systemd service `lgsm-dashboard` (with `ProtectSystem=true` and a restricted capability set),
6. optionally opens the port in an active UFW or firewalld firewall (new installations only),
7. restarts the service, checks that it runs and prints the URL (and the generated `admin` password on a new installation).

Running the installer again updates an existing installation; configuration and users are kept. Installations of older versions that kept a Git checkout in `/opt/lgsm-dashboard` are converted automatically.

### Development builds

Builds from source are meant for testing. They report a version like `dev-main-1a2b3c4`, and the dashboard does not offer release updates for them, so a test build is never replaced by accident. To go back to the release, run the installer with option 1 (or `install` without further flags).

### Manual installation

```bash
git clone https://github.com/yourdawi/linuxgsm-dashboard.git
cd linuxgsm-dashboard
go build -trimpath -o lgsm-dashboard .
sudo ./lgsm-dashboard -port 8080
```

On the first start the dashboard creates `config/config.json` with an `admin` user and prints a random password to the console. Write it down and change it after the first login.

### Docker

The dashboard is also available as a container image. In this **Docker mode** every game server runs as its own container of the official LinuxGSM images (`gameservermanagers/gameserver`), which the dashboard creates and manages through the Docker socket. See [Host mode and Docker mode](#host-mode-and-docker-mode) for the differences.

Create the directory for the game server data first; it has to exist on the host:

```bash
sudo mkdir -p /srv/lgsm
```

#### With docker compose (recommended)

```bash
curl -fsSLO https://raw.githubusercontent.com/yourdawi/linuxgsm-dashboard/main/docker-compose.yml
docker compose up -d
docker compose logs dashboard   # shows the generated admin password on the first start
```

#### With docker run

```bash
docker run -d --name lgsm-dashboard --restart unless-stopped \
  -p 8080:8080 \
  -v lgsm-dashboard-config:/config \
  -v /srv/lgsm:/srv/lgsm \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --security-opt no-new-privileges:true \
  --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add FOWNER \
  ghcr.io/yourdawi/linuxgsm-dashboard:latest
docker logs lgsm-dashboard      # shows the generated admin password on the first start
```

Then open `http://<server>:8080`, log in as `admin` and set the time zone for schedules under **Settings > General** (the default is UTC, or `TZ` if it is set for the container). Additional [command line flags](#command-line-flags) can be appended after the image name, for example `ghcr.io/yourdawi/linuxgsm-dashboard:latest -secure-cookies -trust-proxy` (in `docker-compose.yml` as `command:`).

| Mount | Purpose |
|-------|---------|
| `lgsm-dashboard-config:/config` | Dashboard configuration: users, audit log, tags, schedules and the schedule time zone |
| `/srv/lgsm:/srv/lgsm` | Game server data, one directory per server. It must have the **same path** on the host and in the container, because the dashboard passes it to Docker when it creates game server containers. To use another directory, change both sides and the environment variable `LGSM_DATA_DIR` together. |
| `/var/run/docker.sock` | Access to the Docker engine |

**Security:** access to the Docker socket is equivalent to root access on the host. Everybody with administrator rights in the dashboard can therefore control the host. Keep the dashboard behind HTTPS, use strong passwords and two-factor authentication. Instead of the socket, `DOCKER_HOST=tcp://...` can point to a [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy).

#### Building the image yourself

```bash
git clone https://github.com/yourdawi/linuxgsm-dashboard.git
cd linuxgsm-dashboard
docker build -t ghcr.io/yourdawi/linuxgsm-dashboard:latest .
docker compose up -d
```

#### Updating the container

```bash
docker compose pull && docker compose up -d
```

Without compose: `docker pull ghcr.io/yourdawi/linuxgsm-dashboard:latest`, then `docker stop lgsm-dashboard`, `docker rm lgsm-dashboard` and the `docker run` command above again. The configuration stays in the `lgsm-dashboard-config` volume, and the game server containers keep running while the dashboard is replaced. The in-app update button is not available in Docker mode.

## Host mode and Docker mode

The dashboard picks the mode by itself: inside a container it uses Docker mode, otherwise host mode (details under [Docker mode in detail](#docker-mode-in-detail)).

| | Host mode (installer) | Docker mode |
|---|---|---|
| Dashboard installation | `install-dashboard.sh`, systemd service | `docker compose` or `docker run` |
| Game servers run as | one Linux user per server, LinuxGSM in its home directory | one container per server from `gameservermanagers/gameserver:<game>` |
| Server discovery | home directories, users with UID 1000 or higher, additional server directories | containers with the `lgsm-dashboard.*` labels |
| Install game servers | Yes, Linux user, optional installation directory | Yes, server name; the image is pulled and the data stored in `<LGSM_DATA_DIR>/<name>` |
| Game dependencies | installed by LinuxGSM during the installation (temporary `sudo`) | included in the image |
| Clone instance | Yes, new instance script, shares `serverfiles` | Yes, new container with a copy of `serverfiles` and the configs |
| Delete server | Yes, including the Linux user if it is not shared | Yes, container and data directory |
| Start, stop, restart, update, validate and all other LinuxGSM commands, bulk actions | Yes | Yes, through `docker exec` |
| Game-specific actions (wipes, mods, MTA resources, TS3 password, map compressor) | Yes | Yes |
| FastDL under `/fastdl/<server>/` | Yes | Yes |
| List of installed mods | Yes | Yes |
| Live console and logs | Yes | Yes |
| Live query (players, map) and port check | Yes | Yes, through the Docker host |
| Config editor, file manager | Yes | Yes |
| Backups: create, restore, upload, download, retention | Yes | Yes |
| Cloud sync with rclone | Yes, rclone on the host | No, rclone is not part of the official images |
| Notifications, stop mode | Yes | Yes |
| Scheduler: monitor, update checks, daily `force-update`, weekly `update-lgsm`, automatic backups | Yes, crontab of the server user | Yes, run by the dashboard |
| Time zone of the schedules | time zone of the host | **Settings > General** (default: `TZ` or UTC) |
| Restart after a crash | cron job `monitor` | health check of the official images, plus the optional `monitor` schedule |
| Autostart at boot | systemd service per server | restart policy `unless-stopped` |
| CPU, RAM and disk usage per server | Yes, per Linux user | Yes, per container |
| Host monitoring (CPU, RAM, disk) | Yes | Yes, (disk: the file system of the dashboard container) |
| Firewall status and opening ports | Yes, UFW/firewalld | No, manage the firewall on the host |
| Additional server directories, custom installation directory | Yes | No, all servers live in `LGSM_DATA_DIR` |
| Users, permissions, two-factor authentication, audit log | Yes | Yes |
| Dashboard updates | Yes, signed in-app updates | pull a new image |

## Docker mode in detail

**Installing** a game asks for a server name (3-16 characters) instead of a Linux user. The dashboard then:

1. pulls `gameservermanagers/gameserver:<shortname>` (for example `:vh` for `vhserver`; another repository can be set with `LGSM_GAMESERVER_IMAGE`),
2. creates `<LGSM_DATA_DIR>/<name>` and a `common.cfg` with `backupdir="/data/backup"` and `webdir="/data/public_html"`, so backups and FastDL files stay in the data directory,
3. creates the container `<name>` with host networking, restart policy `unless-stopped`, a stop timeout of 180 seconds, `UPDATE_CHECK=0` and `UPDATE_ON_START=false` (updates are run from the dashboard), and mounts `<name>/mods` as `/app/lgsm/mods`: LinuxGSM keeps the list of installed mods there, outside of `/data`, and would otherwise lose it when the container is re-created,
4. starts it and streams the container log until LinuxGSM has installed and started the server.

**Cloning** creates the container `<name>-<suffix>` with a copy of `serverfiles`, the LinuxGSM configs and the list of installed mods and its own, empty instance config, like a LinuxGSM multi-instance on a host. With host networking the clone uses the same ports as the original until they are changed (for example in `serverfiles/` or the instance config), so it cannot start while the original runs. Stop the original before cloning for a consistent copy of its files.

**Deleting** a server removes its container, its data directory (including backups in `/data/backup`) and its schedules.

**Schedules** are run by the dashboard itself, because the crontab inside a container is reset on every container start. They are set up in the same scheduler as on a host and stored in `/config/schedules/<server>.cron`, outside the reach of the game server containers. Once a minute the dashboard runs the due LinuxGSM commands with `docker exec`; commands due for the same server run one after the other, and they are skipped while the commands of an earlier minute are still running. The time zone is set under **Settings > General** and kept in `/config`; without a setting the dashboard uses `TZ` of its container, or UTC.

**Networking:** game server containers use host networking, so their ports are the ports configured in LinuxGSM, as on a host. The dashboard itself runs on a normal bridge network with the port mapping `8080:8080`; it queries the game servers (players, map) through its gateway, which is the Docker host. If a host firewall blocks that traffic and the game ports are not open, the dashboard can use host networking as well: replace `ports:` with `network_mode: host` (or `-p 8080:8080` with `--network host`); it then listens on port 8080 of the host (`-port` changes it).

**Game server containers** are recognised by their labels and use the data layout of the official images (`/data` with `config-lgsm`, `data`, `log`, `serverfiles`):

| Label | Value |
|-------|-------|
| `lgsm-dashboard.managed` | `true` |
| `lgsm-dashboard.gameserver` | LinuxGSM script, for example `vhserver` |
| `lgsm-dashboard.dir` | The host directory mounted as `/data`, below `LGSM_DATA_DIR`, for example `/srv/lgsm/vhserver` |

Commands run inside the container as the `linuxgsm` user. Stopping a server through LinuxGSM keeps its container running (the official images then report the container as "unhealthy"). **Start** on a stopped container starts the container, whose entrypoint starts the server. For containers not created by the dashboard, LinuxGSM keeps backups inside the container unless `backupdir="/data/backup"` is set in `common.cfg`. A restore needs free space for an extra copy of the backup content while it runs. The dashboard does not re-create game server containers when a newer image is published; game files are updated with LinuxGSM's `update` as usual.

**How the mode is chosen:**

| `LGSM_RUNTIME` | Behaviour |
|----------------|-----------|
| not set or `auto` | Docker mode when the dashboard runs inside a container (`/.dockerenv`, `/run/.containerenv` or a container cgroup), otherwise host mode |
| `docker` | Docker mode (set in the image). The dashboard stops with an error when the Docker engine is not reachable. |
| `host` | Host mode, also inside a container |

In Docker mode the settings show "PRODUCTION (Docker)". Firewall management, dashboard updates, additional server directories, the installation directory field and the systemd autostart are hidden; the cloud sync stays visible but reports that rclone is missing.

Images are built from the signed release binaries and published to `ghcr.io/yourdawi/linuxgsm-dashboard` (tags `latest`, `X.Y` and `X.Y.Z`) by the release workflow once the repository variable `PUBLISH_DOCKER_IMAGE` is set to `true`.

## Configuration

### Command line flags

| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `8080` (from `config.json`) | Port the dashboard listens on. |
| `-config-dir` | `./config` | Directory for the dashboard configuration (see below). |
| `-secure-cookies` | `false` | Always mark cookies as `Secure`. Enable when the dashboard is only reachable via HTTPS. |
| `-trust-proxy` | `false` | Trust `X-Forwarded-For` and `X-Forwarded-Proto` from a reverse proxy. Used for the client IP in rate limiting and the audit log, and for `Secure` cookies and HSTS behind HTTPS. Only enable this behind a proxy you control. |
| `-max-upload-mb` | `4096` | Maximum size of backup and file manager uploads in MiB. |
| `-max-archive-gb` | `1024` | Maximum uncompressed size of a backup archive that may be restored, in GiB. |
| `-version` | | Print the version and exit. |

### Files in the configuration directory

The directory is created with mode `0700`; files containing settings are written with mode `0600`.

| File | Content |
|------|---------|
| `config.json` | Port and dashboard users (Argon2id password hashes, roles, permissions, 2FA secrets and hashed recovery codes) |
| `audit_log.json` | Audit log |
| `server_tags.json` | Server tags |
| `server_paths.json` | Additional server directories |
| `games_cache.json`, `icons/` | Cached game list and icons from linuxgsm.com |
| `schedules/<server>.cron`, `schedule_settings.json` | Docker mode only: schedules of the game servers and their time zone |

### HTTPS

The dashboard itself serves plain HTTP. For access over the internet, put it behind a reverse proxy with TLS (for example nginx or Caddy) and start it with `-secure-cookies -trust-proxy`.

## Game servers outside of /home

LinuxGSM servers do not have to live in `/home`. The dashboard looks for LinuxGSM scripts in:

1. the home directory of every user below `/home/`,
2. the home directory of every regular user (UID 1000 or higher), wherever it is located, for example a user created with `useradd -m -d /mnt/ssd/ark arkserver`,
3. additional server directories configured by an administrator under **Settings > General > Additional server directories**.

For the additional directories, each listed directory and its direct subdirectories are scanned, so `/mnt/ssd/gameservers` also finds `/mnt/ssd/gameservers/ark/arkserver`. The owner of a directory is the Linux user the server runs as. Directories owned by `root`, and scripts that do not belong to the directory owner, are ignored.

New servers can be installed into a custom directory by filling in **Installation directory** in the game installer, for example `/mnt/ssd/gameservers/valheim`. The directory must not exist yet, but its parent directory (for example the mount point of the SSD) must exist.

For all custom paths:

- only letters, digits and `/ . _ - + @` are allowed,
- system locations such as `/etc`, `/usr`, `/root`, `/var/lib` or `/tmp` are rejected,
- if `serverfiles` is a symlink to another disk, it is accepted as long as the target belongs to the server's Linux user.

## Users and permissions

There are two roles:

- **admin** has every permission on every server.
- **user** only sees the servers assigned to them and only has the permissions that were granted. A user without any permission can view the assigned servers but not change anything.

| Permission | Allows |
|------------|--------|
| (server assigned) | View the server and its status, port check, list of installed mods, view the cron schedule and the stop mode |
| `start`, `stop`, `restart` | The corresponding action, also in bulk actions |
| `console` | Live console and logs, sending console commands, kicking and banning players |
| `config` | Config editor (config file types only) |
| `files` | File manager in `serverfiles/` (list, view, edit, upload, download) |
| `backup` | Create, list, download, upload, delete and restore backups, retention settings, cloud sync, `details` |
| `schedule` | Change cron schedules, including the automatic backup schedule |
| `alerts` | View and change notification settings |

Only administrators can manage users, install, delete and clone servers, set tags, install systemd services and cron jobs, change the stop mode and the cloud backup target, run maintenance actions (`update`, `validate`, wipes, mod installation, `debug`, `postdetails` and others), open firewall ports through the API, view the audit log, configure server directories and update the dashboard.

Note that `config` and `files` give a lot of control: LinuxGSM `.cfg` files are executed by bash and the game server loads its own files. Only grant these permissions to people you would also give shell access as the game server user.

## Two-factor authentication

Every user can protect the own account with a one-time code from an authenticator app such as Google Authenticator, Microsoft Authenticator, Authy, Aegis, 2FAS, 1Password or Bitwarden (TOTP, RFC 6238). It is optional and configured by each user under **Settings > General > Two-factor authentication**:

1. Enter the current password and click **Set up**.
2. Scan the QR code (or enter the key manually) and enter the code the app shows. Two-factor authentication is only active after this confirmation.
3. Save the 10 recovery codes. They are shown only once; each code works exactly once.

After that, signing in asks for the code after the password. Without the phone, a recovery code can be entered instead. New recovery codes can be created and two-factor authentication can be disabled in the same card (both need the password and a current code).

Two-factor authentication is independent for every user: each account has its own key and its own recovery codes. After the password step the server issues a short-lived token that is bound to that account, and the code step does not accept a username, so a code is only ever checked against the account whose password was entered. Recovery codes are stored as hashes bound to the username.

**Lost device:** sign in with a recovery code, or ask an administrator. Administrators can remove two-factor authentication from an account under **Users > Reset 2FA**; the user is signed out and can then sign in with the password and set it up again. If the only administrator has lost both device and recovery codes, the entries `totp_enabled`, `totp_secret`, `totp_last_step` and `recovery_codes` of that user can be removed from `config/config.json` on the server (stop the service first).

The codes depend on the time, so the server clock must be correct (NTP). Codes from 30 seconds before or after are accepted, a code can only be used once.

## Security

The dashboard runs as root, so all requests are checked on the server side; the interface only hides what a user cannot use.

**Access control**
- One central authorization check for all server-related requests: server assignment first, then the required permission. Bulk actions check every selected server.
- Unknown actions are rejected; only allowlisted LinuxGSM commands can be run.

**Login and sessions**
- Passwords are stored as Argon2id hashes. Hashes from older versions (unsalted SHA-256) are converted automatically at the next successful login.
- Failed logins are limited per IP address and per user name with a growing waiting time (5 free attempts, then from 2 seconds up to 15 minutes).
- Session cookies are `HttpOnly` and `SameSite=Lax`. Sessions expire after 24 hours of inactivity and after 7 days at the latest. Logging out, changing or resetting a password, enabling, disabling or resetting two-factor authentication and deleting a user end the affected sessions.
- Optional two-factor authentication (TOTP) with one-time recovery codes; the code step has its own attempt limit and rate limiting.

**Browser**
- All actions that change something use `POST` and require a CSRF token bound to the session. Requests from other origins are rejected.
- Security headers including a Content Security Policy, `X-Frame-Options: DENY` and `nosniff`. No CORS headers are sent.
- Files that users can control (FastDL, file manager downloads, backups) are delivered as downloads in a sandbox, never as pages of the dashboard.

**Files and commands**
- Every path from a request is resolved against its intended directory. Path traversal, absolute paths and symlinks that point outside are rejected, and files are opened without following a final symlink. Files written by the dashboard are created through a temporary file and handed over to the server user.
- Values that end up in LinuxGSM configs or crontabs (alerts, backup settings, schedules, stop mode, cloud settings) are validated. Shell arguments are quoted, and console input is limited to a single line.
- Backup archives are checked before a restore: no absolute paths, no `..`, no device files, no links pointing outside the server directory, limits for the number of entries and the uncompressed size, and enough free disk space. The archive is extracted as the server user.
- Request bodies and uploads have size limits; uploads are streamed to disk instead of being held in memory.
- HTTP timeouts are set per request (60 s reading, 120 s writing); live output and large downloads are exempt.

To report a security problem, please open an issue on GitHub without publishing exploit details, or contact the maintainer directly.

## Updating

In Docker mode the dashboard is updated by pulling a new image, see [Updating the container](#updating-the-container). For the native installation:

The dashboard checks GitHub every 6 hours for a new release. When one is available, administrators see an **Update** badge next to **Settings** in the sidebar, and **Settings > General > Dashboard updates** offers to install it. The update button:

- only installs the latest stable release of `yourdawi/linuxgsm-dashboard`,
- checks through the GitHub API that the release commit is part of the `main` branch,
- downloads the binary for the host architecture and verifies the Ed25519 signature of the checksum file with the public key built into the dashboard, then the SHA-256 checksum of the binary,
- starts the new binary once to check that it reports the expected version,
- replaces the running binary, keeps the previous one as `lgsm-dashboard.old` and restarts the service.

Releases without signed binaries, releases from other branches and pre-releases are never offered. Development builds (see [Development builds](#development-builds)) do not get update notices.

Updating with the installer (option 1) works as well and does the same checks.

When upgrading from version 1.0.7 or older:

- the old update button compiles the new version from source one last time; afterwards the dashboard reports the version `dev` and offers the signed release, which switches it to the release binaries,
- alternatively run the installer once,
- all users are logged out once,
- users who changed cron schedules or notifications before need the new `schedule` or `alerts` permission.


## Known limitations

- Player names are only available where the game reports them (A2S player list, Minecraft player sample). Kick and ban send `kick <name>` / `ban <name>` to the console, which not every game understands.
- The live query uses the local address or the `Server IP` reported by LinuxGSM (in Docker mode also the Docker host); servers that block queries from the host itself show no player data.
- The file manager edits files as plain text, without syntax highlighting, and opens files up to 5 MB in the editor.
- During the installation of a new game server, the new Linux user temporarily gets passwordless `sudo`, because LinuxGSM installs missing system packages itself. The permission is removed when the installation ends and on the next start of the dashboard.
- CPU and RAM are measured per Linux user, so instances that share a user show the same values. The same applies to disk usage when instances share a directory (marked in the tooltip).

## Development

```bash
go build ./...
go vet ./...
go test ./...
```

Some tests (file permissions, symlinks, server discovery) only run on Linux and are skipped on other systems; the **Tests** workflow on GitHub runs them on every push. All code of the `main` package is kept in `main.go`, because dashboard versions up to 1.0.7 update themselves with `go build main.go`.

Project layout:

| Path | Content |
|------|---------|
| `main.go` | HTTP server, routing, permission policy, request handlers |
| `backend/` | Server discovery, LinuxGSM actions, files, backups, queries, disk usage, authentication, self-update |
| `ui/` | Web interface (embedded into the binary) |
| `tools/releasekey/` | Key generation and signing for releases |
| `.github/workflows/` | Tests and release builds |

## Disclaimer

This is an independent community project. It is not affiliated with or endorsed by the LinuxGSM project or its developers. LinuxGSM is a trademark of its respective owners, and all other product names belong to their respective owners.
