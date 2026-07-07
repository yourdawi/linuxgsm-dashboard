// LinuxGSM WebAdmin Frontend Application Logic

// Curated list of popular LinuxGSM supported game servers
const popularGames = [
    { id: 'ark', name: 'Ark: Survival Evolved', cmd: 'arkserver', category: 'Survival' },
    { id: 'valheim', name: 'Valheim', cmd: 'vhserver', category: 'Survival' },
    { id: 'cs2', name: 'Counter-Strike 2', cmd: 'cs2server', category: 'Shooter' },
    { id: 'rust', name: 'Rust', cmd: 'rustserver', category: 'Survival' },
    { id: 'minecraft', name: 'Minecraft', cmd: 'mcserver', category: 'Sandbox' },
    { id: 'sdtd', name: '7 Days to Die', cmd: 'sdtdserver', category: 'Survival' },
    { id: 'gmod', name: 'Garry\'s Mod', cmd: 'gmodserver', category: 'Sandbox' },
    { id: 'terraria', name: 'Terraria', cmd: 'tserver', category: 'Sandbox' },
    { id: 'factorio', name: 'Factorio', cmd: 'fctrserver', category: 'Strategy' },
    { id: 'tf2', name: 'Team Fortress 2', cmd: 'tf2server', category: 'Shooter' },
    { id: 'squad', name: 'Squad', cmd: 'squadserver', category: 'Shooter' },
    { id: 'l4d2', name: 'Left 4 Dead 2', cmd: 'l4d2server', category: 'Shooter' },
    { id: 'insurgency', name: 'Insurgency', cmd: 'insserver', category: 'Shooter' },
    { id: 'dst', name: 'Don\'t Starve Together', cmd: 'dstserver', category: 'Survival' },
    { id: 'conan', name: 'Conan Exiles', cmd: 'ceserver', category: 'Survival' },
    { id: 'dayz', name: 'DayZ', cmd: 'dayzserver', category: 'Survival' },
    { id: 'arma3', name: 'Arma 3', cmd: 'arma3server', category: 'Shooter' },
    { id: 'satisfactory', name: 'Satisfactory', cmd: 'sfserver', category: 'Sandbox' },
    { id: 'palworld', name: 'Palworld', cmd: 'pwserver', category: 'Survival' },
    { id: 'projectzomboid', name: 'Project Zomboid', cmd: 'pzserver', category: 'Survival' }
];

// App State
const state = {
    servers: [],
    selectedConsoleServer: '',
    consoleMode: 'tmux', // tmux or log
    consoleInterval: null,
    metricsInterval: null,
    dashboardInterval: null,
    metricsHistory: {
        cpu: [],
        ram: [],
        timestamps: []
    },
    activeConfigServer: null,
    activeConfigFile: null,
    activeConfigContent: '',
    configMode: 'form', // form or raw
    parsedConfigItems: [],
    theme: 'dark', // dark or light
    language: 'en', // en or de
    games: [],
    lastActiveView: 'dashboard'
};

// DOM Elements
const el = {
    loginContainer: document.getElementById('login-container'),
    appContainer: document.getElementById('app-container'),
    loginForm: document.getElementById('login-form'),
    usernameInput: document.getElementById('username'),
    passwordInput: document.getElementById('password'),
    loginError: document.getElementById('login-error'),
    btnLogout: document.getElementById('btn-logout'),
    
    // Views
    views: document.querySelectorAll('.app-view'),
    menuItems: document.querySelectorAll('.menu-item'),
    
    // Dashboard View
    serversContainer: document.getElementById('servers-container'),
    searchServers: document.getElementById('search-servers'),
    btnRefreshDashboard: document.getElementById('btn-refresh-dashboard'),
    statsTotal: document.getElementById('stats-total'),
    statsRunning: document.getElementById('stats-running'),
    statsStopped: document.getElementById('stats-stopped'),
    
    // Installer View
    gamesContainer: document.getElementById('games-container'),
    searchGames: document.getElementById('search-games'),
    installFormContainer: document.getElementById('install-form-container'),
    installGameTitle: document.getElementById('install-game-title'),
    installGameCmd: document.getElementById('install-game-cmd'),
    installGameId: document.getElementById('install-game-id'),
    installForm: document.getElementById('install-form'),
    installUsername: document.getElementById('install-username'),
    installPassword: document.getElementById('install-password'),
    
    // Monitor View
    cpuGauge: document.getElementById('cpu-gauge'),
    cpuVal: document.getElementById('cpu-val'),
    cpuDetails: document.getElementById('cpu-details'),
    ramGauge: document.getElementById('ram-gauge'),
    ramVal: document.getElementById('ram-val'),
    ramDetails: document.getElementById('ram-details'),
    diskGauge: document.getElementById('disk-gauge'),
    diskVal: document.getElementById('disk-val'),
    diskDetails: document.getElementById('disk-details'),
    monitorServerTable: document.getElementById('monitor-server-table'),
    sysHistoryChart: document.getElementById('sys-history-chart'),
    
    // Console View
    consoleServerSelect: document.getElementById('console-server-select'),
    consoleStatusDot: document.getElementById('console-status-dot'),
    consoleStatusText: document.getElementById('console-status-text'),
    consoleBtnStart: document.getElementById('console-btn-start'),
    consoleBtnStop: document.getElementById('console-btn-stop'),
    consoleBtnRestart: document.getElementById('console-btn-restart'),
    consoleBtnConfig: document.getElementById('console-btn-config'),
    consoleBtnDetails: document.getElementById('console-btn-details'),
    consoleBtnBackup: document.getElementById('console-btn-backup'),
    consoleBtnValidate: document.getElementById('console-btn-validate'),
    consoleModeTmux: document.getElementById('console-mode-tmux'),
    consoleModeLog: document.getElementById('console-mode-log'),
    terminalTitleText: document.getElementById('terminal-title-text'),
    terminalOutput: document.getElementById('terminal-output'),
    terminalAutoscroll: document.getElementById('terminal-autoscroll'),
    terminalClear: document.getElementById('terminal-clear'),
    consoleInputForm: document.getElementById('console-input-form'),
    consoleInputField: document.getElementById('console-input-field'),
    consoleInputSubmit: document.getElementById('console-input-submit'),
    
    // Settings View
    settingsPasswordForm: document.getElementById('settings-password-form'),
    settingsOldPassword: document.getElementById('settings-old-password'),
    settingsNewPassword: document.getElementById('settings-new-password'),
    settingsNewPasswordConfirm: document.getElementById('settings-new-password-confirm'),
    settingsPasswordMessage: document.getElementById('settings-password-message'),
    settingsOs: document.getElementById('settings-os'),
    settingsPid: document.getElementById('settings-pid'),
    settingsMode: document.getElementById('settings-mode'),
    settingsServerSelect: document.getElementById('settings-server-select'),
    settingsToolsArea: document.getElementById('settings-tools-area'),
    templateSystemd: document.getElementById('template-systemd'),
    templateCron: document.getElementById('template-cron'),
    
    // Action Stream Modal
    modalStream: document.getElementById('modal-stream'),
    modalStreamTitle: document.getElementById('modal-stream-title'),
    modalStreamStatus: document.getElementById('modal-stream-status'),
    modalStreamOutput: document.getElementById('modal-stream-output'),
    modalStreamAutoscroll: document.getElementById('modal-stream-autoscroll'),
    modalStreamClose: document.getElementById('modal-stream-close'),
    
    // Config Editor Modal
    modalConfig: document.getElementById('modal-config'),
    modalConfigTitle: document.getElementById('modal-config-title'),
    modalConfigSubtitle: document.getElementById('modal-config-subtitle'),
    modalConfigCloseX: document.getElementById('modal-config-close-x'),
    configFilesList: document.getElementById('config-files-list'),
    editorActiveFile: document.getElementById('editor-active-file'),
    btnConfigSave: document.getElementById('btn-config-save'),
    editorLines: document.getElementById('editor-lines'),
    editorTextarea: document.getElementById('editor-textarea'),

    // Port Checker Modal
    modalPortcheck: document.getElementById('modal-portcheck'),
    modalPortcheckTitle: document.getElementById('modal-portcheck-title'),
    modalPortcheckSubtitle: document.getElementById('modal-portcheck-subtitle'),
    modalPortcheckClose: document.getElementById('modal-portcheck-close'),
    modalPortcheckCloseX: document.getElementById('modal-portcheck-close-x'),
    portcheckLoading: document.getElementById('portcheck-loading'),
    portcheckResults: document.getElementById('portcheck-results'),
    portcheckIp: document.getElementById('portcheck-ip'),
    portcheckTableBody: document.getElementById('portcheck-table-body'),
    
    // Delete Server Modal
    modalDelete: document.getElementById('modal-delete'),
    modalDeleteSubtitle: document.getElementById('modal-delete-subtitle'),
    modalDeleteClose: document.getElementById('modal-delete-close'),
    modalDeleteCloseX: document.getElementById('modal-delete-close-x'),
    deleteConfirmInput: document.getElementById('delete-confirm-input'),
    btnDeleteConfirmSubmit: document.getElementById('btn-delete-confirm-submit'),
    // Config Form Toggles
    editorFormWrapper: document.getElementById('editor-form-wrapper'),
    editorTextWrapper: document.getElementById('editor-text-wrapper'),
    btnConfigForm: document.getElementById('btn-config-form'),
    btnConfigRaw: document.getElementById('btn-config-raw'),
    configModeToggle: document.getElementById('config-mode-toggle'),
    configFormBuilder: document.getElementById('config-form-builder'),
    
    // Theme & Language
    btnThemeToggle: document.getElementById('btn-theme-toggle'),
    themeIconSun: document.getElementById('theme-icon-sun'),
    themeIconMoon: document.getElementById('theme-icon-moon'),
    btnLangToggle: document.getElementById('btn-lang-toggle'),
    langToggleText: document.getElementById('lang-toggle-text'),
    
    // Game Sync
    btnSyncGames: document.getElementById('btn-sync-games'),
    syncGamesText: document.getElementById('sync-games-text'),
    gamesSyncProgressBar: document.getElementById('games-sync-progress-bar'),
    gamesSyncProgressFill: document.getElementById('games-sync-progress-fill'),
    gamesSyncProgressText: document.getElementById('games-sync-progress-text')
};

// -------------------------------------------------------------
// Initialization & Navigation
// -------------------------------------------------------------

document.addEventListener('DOMContentLoaded', () => {
    initApp();
});

function initApp() {
    // Setup event listeners
    window.addEventListener('hashchange', handleRouting);
    
    // Auth listeners
    el.loginForm.addEventListener('submit', handleLogin);
    el.btnLogout.addEventListener('click', handleLogout);
    
    // Dashboard listeners
    el.searchServers.addEventListener('input', renderServersGrid);
    el.btnRefreshDashboard.addEventListener('click', refreshDashboard);
    
    // Installer listeners
    el.searchGames.addEventListener('input', renderGamesList);
    el.installForm.addEventListener('submit', handleGameInstall);
    
    // Console listeners
    el.consoleServerSelect.addEventListener('change', handleConsoleServerChange);
    el.consoleBtnStart.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'start'));
    el.consoleBtnStop.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'stop'));
    el.consoleBtnRestart.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'restart'));
    el.consoleBtnConfig.addEventListener('click', () => openConfigEditor(state.selectedConsoleServer));
    el.consoleBtnDetails.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'details'));
    el.consoleBtnBackup.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'backup'));
    el.consoleBtnValidate.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'validate'));
    
    el.consoleModeTmux.addEventListener('click', () => switchConsoleMode('tmux'));
    el.consoleModeLog.addEventListener('click', () => switchConsoleMode('log'));
    el.terminalClear.addEventListener('click', () => el.terminalOutput.innerHTML = '');
    el.consoleInputForm.addEventListener('submit', handleConsoleInputSubmit);
    
    // Config modal listeners
    el.modalConfigCloseX.addEventListener('click', closeConfigEditor);
    el.btnConfigSave.addEventListener('click', saveConfigFile);
    el.btnConfigForm.addEventListener('click', () => switchConfigMode('form'));
    el.btnConfigRaw.addEventListener('click', () => switchConfigMode('raw'));
    el.editorTextarea.addEventListener('input', updateEditorLineNumbers);
    el.editorTextarea.addEventListener('scroll', () => {
        el.editorLines.scrollTop = el.editorTextarea.scrollTop;
    });
    
    // Settings listeners
    el.settingsPasswordForm.addEventListener('submit', changePassword);
    el.settingsServerSelect.addEventListener('change', handleSettingsServerChange);
    
    // Action modal listeners
    el.modalStreamClose.addEventListener('click', () => {
        el.modalStream.classList.add('hidden');
        refreshDashboard();
    });

    // Port Checker modal listeners
    el.modalPortcheckClose.addEventListener('click', () => el.modalPortcheck.classList.add('hidden'));
    el.modalPortcheckCloseX.addEventListener('click', () => el.modalPortcheck.classList.add('hidden'));

    // Delete Server modal listeners
    el.modalDeleteClose.addEventListener('click', () => el.modalDelete.classList.add('hidden'));
    el.modalDeleteCloseX.addEventListener('click', () => el.modalDelete.classList.add('hidden'));
    el.deleteConfirmInput.addEventListener('input', () => {
        const serverId = el.modalDeleteSubtitle.textContent.trim();
        el.btnDeleteConfirmSubmit.disabled = el.deleteConfirmInput.value.trim() !== serverId;
    });
    el.btnDeleteConfirmSubmit.addEventListener('click', confirmDeleteServer);

    // Theme & Language listeners
    el.btnThemeToggle.addEventListener('click', toggleTheme);
    el.btnLangToggle.addEventListener('click', toggleLanguage);
    el.btnSyncGames.addEventListener('click', syncGamesList);

    // Initialize Theme and Language
    initTheme();
    initLanguage();

    // Initial check for authentication
    checkAuthStatus();
    
    // Populate installer list
    loadGamesList();
}

function handleRouting() {
    const hash = window.location.hash || '#dashboard';
    const viewName = hash.substring(1);
    
    let targetView = document.getElementById(`view-${viewName}`);
    if (!targetView) {
        window.location.hash = '#dashboard';
        return;
    }
    
    // Hide all views, deactivate menu items
    el.views.forEach(v => v.classList.add('hidden'));
    el.menuItems.forEach(item => {
        item.classList.remove('active');
        if (item.getAttribute('href') === hash) {
            item.classList.add('active');
        }
    });
    
    // Show active view
    targetView.classList.remove('hidden');
    state.lastActiveView = viewName;
    
    // Clear dynamic loops
    stopConsolePolling();
    stopMetricsPolling();
    stopDashboardPolling();
    
    // Trigger view-specific entry logic
    if (viewName === 'dashboard') {
        refreshDashboard();
        startDashboardPolling();
    } else if (viewName === 'monitor') {
        refreshMetrics();
        startMetricsPolling();
    } else if (viewName === 'console') {
        populateConsoleDropdown();
        if (state.selectedConsoleServer) {
            startConsolePolling();
        }
    } else if (viewName === 'settings') {
        loadSettingsInfo();
    } else if (viewName === 'installer') {
        loadGamesList();
    }
}

// -------------------------------------------------------------
// Authentication
// -------------------------------------------------------------

async function apiFetch(url, options = {}) {
    try {
        const response = await fetch(url, options);
        if (response.status === 401) {
            // Unauthorized, show login screen
            showLogin();
            throw new Error('Nicht autorisiert');
        }
        return response;
    } catch (err) {
        console.error('API Error:', err);
        throw err;
    }
}

async function checkAuthStatus() {
    try {
        const res = await fetch('/api/servers');
        if (res.status === 200) {
            // Logged in!
            hideLogin();
            handleRouting();
        } else {
            showLogin();
        }
    } catch (e) {
        showLogin();
    }
}

function showLogin() {
    el.loginContainer.classList.remove('hidden');
    el.appContainer.classList.add('hidden');
    stopConsolePolling();
    stopMetricsPolling();
    stopDashboardPolling();
}

function hideLogin() {
    el.loginContainer.classList.add('hidden');
    el.appContainer.classList.remove('hidden');
}

async function handleLogin(e) {
    e.preventDefault();
    el.loginError.style.display = 'none';
    
    const username = el.usernameInput.value;
    const password = el.passwordInput.value;
    
    try {
        const res = await fetch('/api/auth/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username, password })
        });
        
        if (res.status === 200) {
            hideLogin();
            window.location.hash = '#dashboard';
            handleRouting();
        } else {
            const data = await res.json();
            el.loginError.textContent = data.error || 'Ungültige Zugangsdaten';
            el.loginError.style.display = 'block';
        }
    } catch (err) {
        el.loginError.textContent = 'Serververbindung fehlgeschlagen';
        el.loginError.style.display = 'block';
    }
}

async function handleLogout() {
    try {
        await fetch('/api/auth/logout', { method: 'POST' });
    } catch (e) {}
    showLogin();
}

// -------------------------------------------------------------
// Dashboard View (Server list)
// -------------------------------------------------------------

function startDashboardPolling() {
    stopDashboardPolling();
    state.dashboardInterval = setInterval(async () => {
        try {
            const res = await fetch('/api/servers');
            if (res.status === 200) {
                state.servers = await res.json();
                updateStatsOverview();
                renderServersGridQuiet();
            }
        } catch (e) {}
    }, 4000);
}

function stopDashboardPolling() {
    if (state.dashboardInterval) {
        clearInterval(state.dashboardInterval);
        state.dashboardInterval = null;
    }
}

async function refreshDashboard() {
    el.btnRefreshDashboard.classList.add('animate-spin');
    try {
        const res = await apiFetch('/api/servers');
        state.servers = await res.json();
        updateStatsOverview();
        renderServersGrid();
    } catch (e) {
        console.error(e);
    } finally {
        setTimeout(() => el.btnRefreshDashboard.classList.remove('animate-spin'), 600);
    }
}

function updateStatsOverview() {
    const total = state.servers.length;
    const running = state.servers.filter(s => s.status === 'running').length;
    const stopped = state.servers.filter(s => s.status === 'stopped').length;
    
    el.statsTotal.textContent = total;
    el.statsRunning.textContent = running;
    el.statsStopped.textContent = stopped;
}

function renderServersGrid() {
    el.serversContainer.innerHTML = '';
    
    const filter = el.searchServers.value.toLowerCase();
    const filtered = state.servers.filter(s => 
        s.name.toLowerCase().includes(filter) || 
        s.user.toLowerCase().includes(filter) ||
        s.script.toLowerCase().includes(filter)
    );
    
    if (filtered.length === 0) {
        el.serversContainer.innerHTML = `
            <div class="glass-panel" style="grid-column: 1/-1; text-align: center; padding: 3rem;">
                <p class="text-muted">${t('no-servers-found')}</p>
                <a href="#installer" class="btn btn-primary mt-4" style="display:inline-flex;">${t('inst-btn-install')}</a>
            </div>
        `;
        return;
    }
    
    filtered.forEach(server => {
        const card = createServerCard(server);
        el.serversContainer.appendChild(card);
    });
}

// Renders the grid but keeps inputs/focus and updates values smoothly
function renderServersGridQuiet() {
    const filter = el.searchServers.value.toLowerCase();
    const filtered = state.servers.filter(s => 
        s.name.toLowerCase().includes(filter) || 
        s.user.toLowerCase().includes(filter)
    );
    
    filtered.forEach(server => {
        const card = document.getElementById(`server-card-${server.id}`);
        if (card) {
            // Update status badge
            const badge = card.querySelector('.badge');
            const statusDot = card.querySelector('.status-dot');
            
            card.className = `server-card ${server.status}`;
            
            let statusText = server.status;
            if (server.status === 'running') {
                badge.className = 'badge badge-success';
                statusDot.className = 'status-dot status-online';
                statusText = t('status-online');
            } else if (server.status === 'stopped') {
                badge.className = 'badge badge-danger';
                statusDot.className = 'status-dot status-offline';
                statusText = t('status-offline');
            } else {
                badge.className = 'badge badge-warning badge-pulse animate-pulse';
                statusDot.className = 'status-dot status-busy';
                statusText = server.status === 'installing' ? t('status-installing') : t('status-updating');
            }
            badge.innerHTML = `<span class="${statusDot.className}"></span> ${statusText}`;
            
            // Update resource meters
            const cpuMeter = card.querySelector('.cpu-meter-fill');
            const cpuLabel = card.querySelector('.cpu-meter-label');
            const ramMeter = card.querySelector('.ram-meter-fill');
            const ramLabel = card.querySelector('.ram-meter-label');
            
            const cpuVal = server.cpu !== undefined ? server.cpu : 0;
            const ramVal = server.ram !== undefined ? server.ram : 0;
            
            cpuMeter.style.width = `${cpuVal}%`;
            cpuLabel.textContent = `${cpuVal.toFixed(1)}%`;
            ramMeter.style.width = `${ramVal > 0 ? (ramVal / 16 * 100) : 0}%`; // Assuming max 16GB default for visual
            ramLabel.textContent = `${ramVal.toFixed(1)} GB`;
            
            // Enable/disable buttons based on status
            const isBusy = server.status === 'installing' || server.status === 'updating';
            card.querySelectorAll('.btn-server-action').forEach(btn => {
                btn.disabled = isBusy;
            });
        } else {
            // Server was newly detected, full redraw
            renderServersGrid();
        }
    });
}

function createServerCard(server) {
    const card = document.createElement('div');
    card.id = `server-card-${server.id}`;
    card.className = `server-card ${server.status}`;
    
    let badgeClass = 'badge-danger';
    let dotClass = 'status-offline';
    let statusText = t('status-offline');
    let isBusy = false;
    
    if (server.status === 'running') {
        badgeClass = 'badge-success';
        dotClass = 'status-online';
        statusText = t('status-online');
    } else if (server.status === 'installing') {
        badgeClass = 'badge-warning badge-pulse animate-pulse';
        dotClass = 'status-busy';
        statusText = t('status-installing');
        isBusy = true;
    } else if (server.status === 'updating') {
        badgeClass = 'badge-warning badge-pulse animate-pulse';
        dotClass = 'status-busy';
        statusText = t('status-updating');
        isBusy = true;
    }
    
    const cpu = server.cpu || 0;
    const ram = server.ram || 0;
    
    card.innerHTML = `
        <div class="card-header">
            <div class="card-title-group">
                <h3>${server.name}</h3>
                <div class="card-subtitle">
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"></path><circle cx="12" cy="7" r="4"></circle></svg>
                    <span>${server.user}</span>
                    <span class="text-muted">| ${t('card-port')}: ${server.port || '--'}</span>
                </div>
            </div>
            <span class="badge ${badgeClass}">
                <span class="status-dot ${dotClass}"></span>
                ${statusText}
            </span>
        </div>
        
        <div class="card-resources">
            <div class="meter-group">
                <div class="meter-label">
                    <span>CPU</span>
                    <span class="cpu-meter-label">${cpu.toFixed(1)}%</span>
                </div>
                <div class="meter-bar">
                    <div class="meter-fill cpu-meter-fill" style="width: ${cpu}%"></div>
                </div>
            </div>
            
            <div class="meter-group">
                <div class="meter-label">
                    <span>RAM</span>
                    <span class="ram-meter-label">${ram.toFixed(1)} GB</span>
                </div>
                <div class="meter-bar">
                    <div class="meter-fill ram-meter-fill" style="width: ${ram > 0 ? Math.min((ram / 8 * 100), 100) : 0}%"></div>
                </div>
            </div>
        </div>
        
        <div class="card-actions">
            ${server.status === 'running' ? `
                <button class="btn btn-danger btn-server-action" onclick="runServerAction('${server.id}', 'stop')" ${isBusy ? 'disabled' : ''}>
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect></svg> ${t('btn-stop')}
                </button>
            ` : `
                <button class="btn btn-success btn-server-action" onclick="runServerAction('${server.id}', 'start')" ${isBusy ? 'disabled' : ''}>
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg> ${t('btn-start')}
                </button>
            `}
            <button class="btn btn-warning btn-server-action" onclick="runServerAction('${server.id}', 'restart')" ${isBusy ? 'disabled' : ''}>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"></path></svg> ${t('btn-restart')}
            </button>
        </div>
        <div class="card-actions-row-3">
            <button class="btn btn-secondary btn-sm btn-server-action" onclick="openConsole('${server.id}')" ${isBusy ? 'disabled' : ''}>
                ${t('btn-console')}
            </button>
            <button class="btn btn-secondary btn-sm btn-server-action" onclick="openConfigEditor('${server.id}')" ${isBusy ? 'disabled' : ''}>
                ${t('btn-configs')}
            </button>
            <button class="btn btn-secondary btn-sm btn-server-action" onclick="runServerAction('${server.id}', 'update')" ${isBusy ? 'disabled' : ''}>
                ${t('btn-update')}
            </button>
        </div>
        <div class="card-actions-row-3" style="margin-top: 0.4rem;">
            <button class="btn btn-secondary btn-sm btn-server-action" onclick="runServerAction('${server.id}', 'details')" ${isBusy ? 'disabled' : ''}>
                ${t('btn-details')}
            </button>
            <button class="btn btn-secondary btn-sm btn-server-action" onclick="runServerAction('${server.id}', 'backup')" ${isBusy ? 'disabled' : ''}>
                ${t('btn-backup')}
            </button>
            <button class="btn btn-secondary btn-sm btn-server-action" onclick="runServerAction('${server.id}', 'validate')" ${isBusy ? 'disabled' : ''}>
                ${t('btn-validate')}
            </button>
        </div>
        <div class="card-actions-row-2" style="margin-top: 0.4rem; display: grid; grid-template-columns: 1fr 1fr; gap: 0.4rem;">
            <button class="btn btn-primary btn-sm btn-server-action" onclick="checkServerPorts('${server.id}')" style="display: flex; align-items: center; justify-content: center; gap: 4px; padding: 0.35rem 0.25rem; font-size: 0.75rem;">
                <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="display: inline-block;"><circle cx="12" cy="12" r="10"></circle><path d="M12 16v-4M12 8h.01"></path></svg> ${t('btn-portcheck')}
            </button>
            <button class="btn btn-danger btn-sm btn-server-action" onclick="openDeleteServerModal('${server.id}')" style="display: flex; align-items: center; justify-content: center; gap: 4px; padding: 0.35rem 0.25rem; font-size: 0.75rem;">
                <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="display: inline-block;"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg> ${t('btn-delete-server')}
            </button>
        </div>
    `;
    
    return card;
}

// -------------------------------------------------------------
// Action Logs Live Stream Handler
// -------------------------------------------------------------

function runServerAction(serverId, action) {
    const tAction = t('btn-' + action) || action.toUpperCase();
    el.modalStreamTitle.textContent = t('modal-stream-title-run').replace('{action}', tAction).replace('{server}', serverId);
    el.modalStreamStatus.textContent = t('status-connecting');
    el.modalStreamStatus.className = 'badge badge-warning animate-pulse';
    el.modalStreamOutput.innerHTML = '';
    el.modalStreamClose.disabled = true;
    el.modalStream.classList.remove('hidden');
    
    // Connect to Server-Sent Events stream for command execution
    const url = `/api/servers/${serverId}/action?action=${action}&lang=${state.language}`;
    const eventSource = new EventSource(url);
    
    eventSource.onopen = () => {
        el.modalStreamStatus.textContent = t('status-running-action');
        el.modalStreamStatus.className = 'badge badge-warning badge-pulse animate-pulse';
    };
    
    eventSource.onmessage = (event) => {
        const data = JSON.parse(event.data);
        if (data.type === 'log') {
            const line = document.createElement('div');
            line.className = 'terminal-row';
            line.textContent = data.text;
            el.modalStreamOutput.appendChild(line);
            
            if (el.modalStreamAutoscroll.checked) {
                el.modalStreamOutput.scrollTop = el.modalStreamOutput.scrollHeight;
            }
        } else if (data.type === 'exit') {
            const exitCode = data.code;
            el.modalStreamStatus.textContent = exitCode === 0 ? t('status-success') : t('status-error');
            el.modalStreamStatus.className = exitCode === 0 ? 'badge badge-success' : 'badge badge-danger';
            el.modalStreamClose.disabled = false;
            eventSource.close();
            
            // If in console tab, refresh console button states
            if (state.lastActiveView === 'console' && state.selectedConsoleServer === serverId) {
                setTimeout(updateConsoleViewButtons, 1000);
            }
        }
    };
    
    eventSource.onerror = (err) => {
        const line = document.createElement('div');
        line.className = 'terminal-row text-danger';
        line.textContent = t('modal-stream-err-run');
        el.modalStreamOutput.appendChild(line);
        el.modalStreamStatus.textContent = t('status-cancelled');
        el.modalStreamStatus.className = 'badge badge-danger';
        el.modalStreamClose.disabled = false;
        eventSource.close();
    };
}

// -------------------------------------------------------------
// Live Console View
// -------------------------------------------------------------

function populateConsoleDropdown() {
    // Keep current selection if valid
    const current = el.consoleServerSelect.value;
    
    el.consoleServerSelect.innerHTML = `<option value="">${t('console-select-default')}</option>`;
    
    state.servers.forEach(server => {
        const opt = document.createElement('option');
        opt.value = server.id;
        opt.textContent = `${server.name} (${server.user})`;
        el.consoleServerSelect.appendChild(opt);
    });
    
    if (current && state.servers.find(s => s.id === current)) {
        el.consoleServerSelect.value = current;
    } else {
        state.selectedConsoleServer = '';
        updateConsoleViewButtons();
    }
}

function openConsole(serverId) {
    state.selectedConsoleServer = serverId;
    window.location.hash = '#console';
    handleRouting();
}

function handleConsoleServerChange() {
    state.selectedConsoleServer = el.consoleServerSelect.value;
    el.terminalOutput.innerHTML = '';
    
    stopConsolePolling();
    updateConsoleViewButtons();
    
    if (state.selectedConsoleServer) {
        startConsolePolling();
    }
}

function updateConsoleViewButtons() {
    const server = state.servers.find(s => s.id === state.selectedConsoleServer);
    
    if (!server) {
        el.consoleStatusDot.className = 'status-dot status-offline';
        el.consoleStatusText.textContent = t('console-no-server-selected');
        el.consoleBtnStart.disabled = true;
        el.consoleBtnStop.disabled = true;
        el.consoleBtnRestart.disabled = true;
        el.consoleBtnConfig.disabled = true;
        el.consoleBtnDetails.disabled = true;
        el.consoleBtnBackup.disabled = true;
        el.consoleBtnValidate.disabled = true;
        el.consoleInputField.disabled = true;
        el.consoleInputSubmit.disabled = true;
        el.terminalTitleText.textContent = t('console-no-server-connected');
        return;
    }
    
    el.terminalTitleText.textContent = `${server.name} Console (${server.user})`;
    
    const isBusy = server.status === 'installing' || server.status === 'updating';
    const isRunning = server.status === 'running';
    
    el.consoleBtnStart.disabled = isRunning || isBusy;
    el.consoleBtnStop.disabled = !isRunning || isBusy;
    el.consoleBtnRestart.disabled = !isRunning || isBusy;
    el.consoleBtnConfig.disabled = isBusy;
    el.consoleBtnDetails.disabled = isBusy;
    el.consoleBtnBackup.disabled = isBusy;
    el.consoleBtnValidate.disabled = isBusy;
    el.consoleInputField.disabled = !isRunning || isBusy;
    el.consoleInputSubmit.disabled = !isRunning || isBusy;
    
    if (isRunning) {
        el.consoleStatusDot.className = 'status-dot status-online';
        el.consoleStatusText.textContent = t('modal-stream-running');
    } else if (isBusy) {
        el.consoleStatusDot.className = 'status-dot status-busy';
        el.consoleStatusText.textContent = server.status === 'installing' ? t('status-installing') : t('status-updating');
    } else {
        el.consoleStatusDot.className = 'status-dot status-offline';
        el.consoleStatusText.textContent = t('status-offline');
    }
}

function startConsolePolling() {
    stopConsolePolling();
    
    // Initial fetch
    fetchConsoleData();
    
    // Poll every 1.5 seconds
    state.consoleInterval = setInterval(fetchConsoleData, 1500);
}

function stopConsolePolling() {
    if (state.consoleInterval) {
        clearInterval(state.consoleInterval);
        state.consoleInterval = null;
    }
}

async function fetchConsoleData() {
    if (!state.selectedConsoleServer) return;
    
    try {
        const res = await apiFetch(`/api/servers/${state.selectedConsoleServer}/console?mode=${state.consoleMode}`);
        const data = await res.json();
        
        if (data.lines) {
            // Rendering the whole buffer or tailing
            if (state.consoleMode === 'tmux') {
                // Tmux gives full screens, overwrite
                el.terminalOutput.innerHTML = '';
                data.lines.forEach(line => {
                    const row = document.createElement('div');
                    row.className = 'terminal-row';
                    row.textContent = line || ' ';
                    el.terminalOutput.appendChild(row);
                });
                
                if (el.terminalAutoscroll.checked) {
                    el.terminalOutput.scrollTop = el.terminalOutput.scrollHeight;
                }
            } else {
                // Log file is append mode, but let's just render the last lines returned
                el.terminalOutput.innerHTML = '';
                data.lines.forEach(line => {
                    const row = document.createElement('div');
                    row.className = 'terminal-row';
                    row.textContent = line;
                    el.terminalOutput.appendChild(row);
                });
                
                if (el.terminalAutoscroll.checked) {
                    el.terminalOutput.scrollTop = el.terminalOutput.scrollHeight;
                }
            }
        }
    } catch (e) {
        console.error('Console fetch error:', e);
    }
}

function switchConsoleMode(mode) {
    state.consoleMode = mode;
    el.consoleModeTmux.classList.toggle('active', mode === 'tmux');
    el.consoleModeLog.classList.toggle('active', mode === 'log');
    
    el.terminalOutput.innerHTML = `<div class="terminal-row text-muted">${t('terminal-loading')}</div>`;
    fetchConsoleData();
}

// -------------------------------------------------------------
// Config Editor Modal
// -------------------------------------------------------------

async function openConfigEditor(serverId) {
    state.activeConfigServer = serverId;
    state.activeConfigFile = null;
    state.activeConfigContent = '';
    state.configMode = 'form';
    state.parsedConfigItems = [];
    
    const server = state.servers.find(s => s.id === serverId);
    el.modalConfigSubtitle.textContent = server ? `${server.name} (${server.user})` : serverId;
    el.configFilesList.innerHTML = `<li class="config-file-item">${t('config-loading-files')}</li>`;
    el.editorActiveFile.textContent = t('modal-config-no-file');
    el.editorTextarea.value = '';
    el.editorTextarea.disabled = true;
    el.btnConfigSave.disabled = true;
    el.editorLines.innerHTML = '1';
    
    // Reset view toggle states
    el.configModeToggle.style.display = 'none';
    el.editorFormWrapper.classList.add('hidden');
    el.editorTextWrapper.classList.remove('hidden');
    el.btnConfigForm.classList.add('active');
    el.btnConfigRaw.classList.remove('active');
    
    el.modalConfig.classList.remove('hidden');
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/configs`);
        const files = await res.json();
        
        el.configFilesList.innerHTML = '';
        if (files.length === 0) {
            el.configFilesList.innerHTML = `<li class="config-file-item text-muted">${t('config-no-files')}</li>`;
            return;
        }
        
        files.forEach(file => {
            const li = document.createElement('li');
            li.className = 'config-file-item';
            li.textContent = file.name;
            li.addEventListener('click', () => loadConfigFile(file.path, file.name, li));
            el.configFilesList.appendChild(li);
        });
    } catch (e) {
        el.configFilesList.innerHTML = `<li class="config-file-item text-danger">${t('config-load-error')}</li>`;
    }
}

async function loadConfigFile(path, name, liElement) {
    // Highlight list item
    el.configFilesList.querySelectorAll('.config-file-item').forEach(li => li.classList.remove('active'));
    liElement.classList.add('active');
    
    el.editorActiveFile.textContent = name;
    el.editorTextarea.value = t('config-loading-file');
    el.editorTextarea.disabled = true;
    el.btnConfigSave.disabled = true;
    
    state.activeConfigFile = path;
    
    try {
        const res = await apiFetch(`/api/servers/${state.activeConfigServer}/configs/file?path=${encodeURIComponent(path)}`);
        const data = await res.json();
        
        state.activeConfigContent = data.content;
        el.editorTextarea.value = data.content;
        el.editorTextarea.disabled = false;
        el.btnConfigSave.disabled = false;
        updateEditorLineNumbers();
        
        // Parse config and build form
        state.parsedConfigItems = parseConfig(data.content, path);
        if (state.parsedConfigItems.length > 0) {
            el.configModeToggle.style.display = 'flex';
            buildConfigForm(state.parsedConfigItems);
            // Show default form view
            switchConfigMode('form');
        } else {
            // Force raw mode if not parseable
            el.configModeToggle.style.display = 'none';
            switchConfigMode('raw');
        }
    } catch (e) {
        el.editorTextarea.value = t('config-load-file-error');
    }
}

async function saveConfigFile() {
    if (!state.activeConfigServer || !state.activeConfigFile) return;
    
    el.btnConfigSave.disabled = true;
    el.btnConfigSave.textContent = t('saving');
    
    let content = '';
    if (state.configMode === 'form') {
        const formValues = {};
        state.parsedConfigItems.forEach(item => {
            const inputEl = document.getElementById(`config-form-val-${item.key}`);
            if (inputEl) {
                if (item.type === 'boolean') {
                    formValues[item.key] = inputEl.checked ? 'true' : 'false';
                } else {
                    formValues[item.key] = inputEl.value;
                }
            }
        });
        
        const ext = state.activeConfigFile.split('.').pop().toLowerCase();
        if (ext === 'json') {
            content = serializeJsonForm(state.activeConfigContent, formValues);
        } else {
            content = serializeConfigForm(state.activeConfigContent, formValues);
        }
    } else {
        content = el.editorTextarea.value;
    }
    
    try {
        const res = await apiFetch(`/api/servers/${state.activeConfigServer}/configs/file`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                path: state.activeConfigFile,
                content: content
            })
        });
        
        if (res.status === 200) {
            state.activeConfigContent = content;
            el.editorTextarea.value = content;
            updateEditorLineNumbers();
            
            el.btnConfigSave.textContent = t('saved');
            setTimeout(() => {
                el.btnConfigSave.textContent = t('modal-config-btn-save');
                el.btnConfigSave.disabled = false;
            }, 1500);
        } else {
            alert(t('config-save-error'));
            el.btnConfigSave.textContent = t('modal-config-btn-save');
            el.btnConfigSave.disabled = false;
        }
    } catch (e) {
        alert(t('connection-error'));
        el.btnConfigSave.textContent = t('modal-config-btn-save');
        el.btnConfigSave.disabled = false;
    }
}

function updateEditorLineNumbers() {
    const lines = el.editorTextarea.value.split('\n').length;
    let gutterHTML = '';
    for (let i = 1; i <= lines; i++) {
        gutterHTML += `<div>${i}</div>`;
    }
    el.editorLines.innerHTML = gutterHTML;
}

function closeConfigEditor() {
    el.modalConfig.classList.add('hidden');
    state.activeConfigServer = null;
    state.activeConfigFile = null;
}

// -------------------------------------------------------------
// Game Installer View
// -------------------------------------------------------------

function renderGamesList() {
    el.gamesContainer.innerHTML = '';
    const filter = el.searchGames.value.toLowerCase();
    
    const sourceGames = state.games.length > 0 ? state.games : popularGames;
    
    const filtered = sourceGames.filter(g => 
        g.name.toLowerCase().includes(filter) || 
        g.cmd.toLowerCase().includes(filter)
    );
    
    filtered.forEach(game => {
        const card = document.createElement('div');
        card.className = 'game-card';
        
        let iconHtml = '';
        if (game.icon && game.icon.startsWith('/api/games/icon/')) {
            iconHtml = `<img class="game-card-icon" src="${game.icon}" alt="${game.name}" onerror="this.style.display='none'; this.nextElementSibling.style.display='flex';">`;
        }
        
        card.innerHTML = `
            ${iconHtml}
            <div class="game-icon-placeholder game-card-icon-placeholder" style="display: ${game.icon ? 'none' : 'flex'};">
                <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="12 2 2 7 12 12 22 7 12 2"></polygon><polyline points="2 17 12 22 22 17"></polyline><polyline points="2 12 12 17 22 12"></polyline></svg>
            </div>
            <h4>${game.name}</h4>
            <span>${t('label-script')} ${game.cmd}</span>
        `;
        card.addEventListener('click', () => selectGameForInstall(game, card));
        el.gamesContainer.appendChild(card);
    });
}

function selectGameForInstall(game, cardElement) {
    el.gamesContainer.querySelectorAll('.game-card').forEach(c => c.classList.remove('selected'));
    cardElement.classList.add('selected');
    
    el.installGameTitle.textContent = game.name;
    el.installGameCmd.textContent = game.cmd;
    el.installGameId.value = game.id;
    
    // Auto-populate default username (e.g. valheim -> valheimserver)
    el.installUsername.value = `${game.id}server`.replace(/[^a-z0-9_]/g, '');
    el.installPassword.value = '';
    
    el.installFormContainer.classList.remove('hidden');
    
    // Smooth scroll down to form on mobile
    if (window.innerWidth <= 992) {
        el.installFormContainer.scrollIntoView({ behavior: 'smooth' });
    }
}

function handleGameInstall(e) {
    e.preventDefault();
    
    const gameId = el.installGameId.value;
    const gamesList = state.games.length > 0 ? state.games : popularGames;
    const game = gamesList.find(g => g.id === gameId);
    if (!game) return;
    
    const username = el.installUsername.value;
    const password = el.installPassword.value;
    
    el.modalStreamTitle.textContent = t('modal-stream-title-install').replace('{game}', game.name);
    el.modalStreamStatus.textContent = t('status-creating-user');
    el.modalStreamStatus.className = 'badge badge-warning animate-pulse';
    el.modalStreamOutput.innerHTML = '';
    el.modalStreamClose.disabled = true;
    el.modalStream.classList.remove('hidden');
    
    // Connect to EventSource for install logs
    const queryParams = `?game=${encodeURIComponent(game.cmd)}&user=${encodeURIComponent(username)}&password=${encodeURIComponent(password)}&lang=${state.language}`;
    const eventSource = new EventSource(`/api/games/install${queryParams}`);
    
    eventSource.onopen = () => {
        el.modalStreamStatus.textContent = t('status-installing-action');
    };
    
    eventSource.onmessage = (event) => {
        const data = JSON.parse(event.data);
        if (data.type === 'log') {
            const line = document.createElement('div');
            line.className = 'terminal-row';
            line.textContent = data.text;
            el.modalStreamOutput.appendChild(line);
            
            if (el.modalStreamAutoscroll.checked) {
                el.modalStreamOutput.scrollTop = el.modalStreamOutput.scrollHeight;
            }
        } else if (data.type === 'exit') {
            const exitCode = data.code;
            el.modalStreamStatus.textContent = exitCode === 0 ? t('status-success') : t('status-error');
            el.modalStreamStatus.className = exitCode === 0 ? 'badge badge-success' : 'badge badge-danger';
            el.modalStreamClose.disabled = false;
            eventSource.close();
        }
    };
    
    eventSource.onerror = (err) => {
        const line = document.createElement('div');
        line.className = 'terminal-row text-danger';
        line.textContent = t('modal-stream-err-install');
        el.modalStreamOutput.appendChild(line);
        el.modalStreamStatus.textContent = t('status-cancelled');
        el.modalStreamStatus.className = 'badge badge-danger';
        el.modalStreamClose.disabled = false;
        eventSource.close();
    };
}

// -------------------------------------------------------------
// System Monitor & Charting
// -------------------------------------------------------------

function startMetricsPolling() {
    stopMetricsPolling();
    state.metricsInterval = setInterval(refreshMetrics, 3000);
}

function stopMetricsPolling() {
    if (state.metricsInterval) {
        clearInterval(state.metricsInterval);
        state.metricsInterval = null;
    }
}

async function refreshMetrics() {
    try {
        const res = await apiFetch('/api/system/stats');
        const data = await res.json();
        state.lastMetricsData = data;
        
        // Update circular gauges
        updateGauge(el.cpuGauge, el.cpuVal, data.host.cpu);
        updateGauge(el.ramGauge, el.ramVal, data.host.ramPercent);
        updateGauge(el.diskGauge, el.diskVal, data.host.diskPercent);
        
        renderMetricsDetails(data);
        
        // Update chart history
        const now = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
        state.metricsHistory.timestamps.push(now);
        state.metricsHistory.cpu.push(data.host.cpu);
        state.metricsHistory.ram.push(data.host.ramPercent);
        
        // Limit history to 60 data points (3 mins)
        if (state.metricsHistory.timestamps.length > 60) {
            state.metricsHistory.timestamps.shift();
            state.metricsHistory.cpu.shift();
            state.metricsHistory.ram.shift();
        }
        
        drawSystemHistoryChart();
        
        // Update per-server resource table
        renderMonitorServerTable(data.servers);
    } catch (e) {
        console.error('Failed to fetch metrics:', e);
    }
}

function renderMetricsDetails(data) {
    if (!data || !data.host) return;
    el.cpuDetails.textContent = `${t('overview-cores')}: ${data.host.cpuCores} | ${t('overview-clock')}: ${data.host.cpuModel || '--'}`;
    el.ramDetails.textContent = `${t('overview-used')}: ${data.host.ramUsed.toFixed(1)} GB / ${data.host.ramTotal.toFixed(1)} GB`;
    el.diskDetails.textContent = `${t('overview-free')}: ${data.host.diskFree.toFixed(1)} GB / ${data.host.diskTotal.toFixed(1)} GB`;
}

function updateGauge(circleEl, valEl, percent) {
    const rounded = Math.round(percent);
    // stroke-dasharray="percent, 100"
    circleEl.setAttribute('stroke-dasharray', `${rounded}, 100`);
    valEl.textContent = `${rounded}%`;
    
    // Change color based on severity
    if (rounded > 85) {
        circleEl.style.stroke = 'var(--danger)';
    } else if (rounded > 70) {
        circleEl.style.stroke = 'var(--warning)';
    } else {
        // default back to original stylesheets
        circleEl.style.stroke = '';
    }
}

function drawSystemHistoryChart() {
    const canvas = el.sysHistoryChart;
    if (!canvas) return;
    
    // Fit canvas width to container
    const rect = canvas.parentNode.getBoundingClientRect();
    canvas.width = rect.width;
    canvas.height = 200;
    
    const ctx = canvas.getContext('2d');
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    
    const cpuData = state.metricsHistory.cpu;
    const ramData = state.metricsHistory.ram;
    const len = cpuData.length;
    
    if (len === 0) return;
    
    const padding = { top: 15, right: 15, bottom: 25, left: 40 };
    const chartWidth = canvas.width - padding.left - padding.right;
    const chartHeight = canvas.height - padding.top - padding.bottom;
    
    // Draw Grid Lines & Y Labels
    ctx.strokeStyle = 'rgba(255, 255, 255, 0.05)';
    ctx.lineWidth = 1;
    ctx.fillStyle = 'var(--text-muted)';
    ctx.font = '10px var(--font-sans)';
    ctx.textAlign = 'right';
    ctx.textBaseline = 'middle';
    
    for (let i = 0; i <= 4; i++) {
        const yVal = i * 25; // 0, 25, 50, 75, 100
        const y = padding.top + chartHeight - (yVal / 100) * chartHeight;
        
        ctx.beginPath();
        ctx.moveTo(padding.left, y);
        ctx.lineTo(padding.left + chartWidth, y);
        ctx.stroke();
        
        ctx.fillText(`${yVal}%`, padding.left - 10, y);
    }
    
    // Draw lines helper
    const drawLine = (data, color, fillGradient) => {
        ctx.beginPath();
        const xStep = len > 1 ? chartWidth / (len - 1) : chartWidth;
        
        for (let i = 0; i < len; i++) {
            const x = padding.left + i * xStep;
            const y = padding.top + chartHeight - (data[i] / 100) * chartHeight;
            
            if (i === 0) {
                ctx.moveTo(x, y);
            } else {
                ctx.lineTo(x, y);
            }
        }
        
        // Stroke line
        ctx.strokeStyle = color;
        ctx.lineWidth = 2.5;
        ctx.stroke();
        
        // Fill area
        ctx.lineTo(padding.left + (len - 1) * xStep, padding.top + chartHeight);
        ctx.lineTo(padding.left, padding.top + chartHeight);
        ctx.closePath();
        ctx.fillStyle = fillGradient;
        ctx.fill();
    };
    
    // CPU Gradient
    const cpuGrad = ctx.createLinearGradient(0, padding.top, 0, padding.top + chartHeight);
    cpuGrad.addColorStop(0, 'rgba(124, 77, 255, 0.15)');
    cpuGrad.addColorStop(1, 'rgba(124, 77, 255, 0.01)');
    drawLine(cpuData, '#7c4dff', cpuGrad);
    
    // RAM Gradient
    const ramGrad = ctx.createLinearGradient(0, padding.top, 0, padding.top + chartHeight);
    ramGrad.addColorStop(0, 'rgba(0, 176, 255, 0.12)');
    ramGrad.addColorStop(1, 'rgba(0, 176, 255, 0.01)');
    drawLine(ramData, '#00b0ff', ramGrad);
    
    // Draw X labels (Timestamps, show 5 labels max)
    if (len > 0) {
        ctx.fillStyle = 'var(--text-muted)';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'top';
        const labelCount = Math.min(len, 5);
        const indexStep = Math.max(1, Math.floor(len / labelCount));
        
        for (let i = 0; i < len; i += indexStep) {
            const x = padding.left + i * (chartWidth / (len - 1));
            ctx.fillText(state.metricsHistory.timestamps[i], x, padding.top + chartHeight + 8);
        }
    }
}

function renderMonitorServerTable(serversMetrics) {
    el.monitorServerTable.innerHTML = '';
    
    state.lastServersMetrics = serversMetrics;
    el.monitorServerTable.innerHTML = '';
    
    if (!serversMetrics || serversMetrics.length === 0) {
        el.monitorServerTable.innerHTML = `
            <tr>
                <td colspan="6" class="text-center text-muted" style="padding:2rem;">${t('overview-no-active-processes')}</td>
            </tr>
        `;
        return;
    }
    
    serversMetrics.forEach(srv => {
        const tr = document.createElement('tr');
        
        let statusBadge = `<span class="badge badge-danger"><span class="status-dot status-offline"></span> ${t('status-offline')}</span>`;
        if (srv.status === 'running') {
            statusBadge = `<span class="badge badge-success"><span class="status-dot status-online"></span> ${t('status-online')}</span>`;
        } else if (srv.status === 'installing' || srv.status === 'updating') {
            const statusLabel = srv.status === 'installing' ? t('status-installing') : t('status-updating');
            statusBadge = `<span class="badge badge-warning badge-pulse animate-pulse"><span class="status-dot status-busy"></span> ${statusLabel}</span>`;
        }
        
        tr.innerHTML = `
            <td><strong>${srv.name}</strong></td>
            <td><code>${srv.user}</code></td>
            <td>${statusBadge}</td>
            <td>${srv.status === 'running' ? `${srv.cpu.toFixed(1)} %` : '--'}</td>
            <td>${srv.status === 'running' ? `${srv.ram.toFixed(2)} GB` : '--'}</td>
            <td>${srv.status === 'running' && srv.pids ? srv.pids.map(p => `<code>${p}</code>`).join(', ') : `<span class="text-muted">${t('overview-none')}</span>`}</td>
        `;
        el.monitorServerTable.appendChild(tr);
    });
}

// -------------------------------------------------------------
// Settings & Config
// -------------------------------------------------------------

async function loadSettingsInfo() {
    try {
        const res = await apiFetch('/api/system/info');
        const data = await res.json();
        
        state.lastSettingsData = data;
        
        el.settingsOs.textContent = data.os;
        el.settingsPid.textContent = data.pid;
        renderSettingsMode(data);
        if (data.mock) {
            el.settingsMode.className = 'info-value highlight text-warning';
        } else {
            el.settingsMode.className = 'info-value highlight text-success';
        }
        
        // Populate tools dropdown
        populateSettingsServerSelect();
    } catch (e) {
        console.error(e);
    }
}

function renderSettingsMode(data) {
    if (!data) return;
    el.settingsMode.textContent = data.mock ? t('settings-mode-mock') : t('settings-mode-prod');
}

async function changePassword(e) {
    e.preventDefault();
    el.settingsPasswordMessage.innerHTML = '';
    
    const oldPassword = el.settingsOldPassword.value;
    const newPassword = el.settingsNewPassword.value;
    const confirmPassword = el.settingsNewPasswordConfirm.value;
    
    if (newPassword !== confirmPassword) {
        el.settingsPasswordMessage.textContent = t('settings-pass-mismatch');
        el.settingsPasswordMessage.className = 'info-message text-danger';
        return;
    }
    
    try {
        const res = await apiFetch('/api/settings/password', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ oldPassword, newPassword })
        });
        
        const data = await res.json();
        if (res.status === 200) {
            el.settingsPasswordMessage.textContent = t('settings-pass-success');
            el.settingsPasswordMessage.className = 'info-message text-success';
            el.settingsPasswordForm.reset();
        } else {
            el.settingsPasswordMessage.textContent = data.error || t('settings-pass-error');
            el.settingsPasswordMessage.className = 'info-message text-danger';
        }
    } catch (e) {
        el.settingsPasswordMessage.textContent = t('connection-error');
        el.settingsPasswordMessage.className = 'info-message text-danger';
    }
}

// -------------------------------------------------------------
// Interactive Console Inputs & Settings Helpers
// -------------------------------------------------------------

async function handleConsoleInputSubmit(e) {
    e.preventDefault();
    const command = el.consoleInputField.value.trim();
    if (!command || !state.selectedConsoleServer) return;
    
    // Clear input immediately
    el.consoleInputField.value = '';
    
    // Visual echo in the terminal
    const cmdEcho = document.createElement('div');
    cmdEcho.className = 'terminal-row';
    cmdEcho.style.color = 'var(--primary)';
    cmdEcho.textContent = `> ${command}`;
    el.terminalOutput.appendChild(cmdEcho);
    if (el.terminalAutoscroll.checked) {
        el.terminalOutput.scrollTop = el.terminalOutput.scrollHeight;
    }
    
    // Mock simulation output
    const isMock = state.servers.find(s => s.id === state.selectedConsoleServer)?.cpu !== undefined; // mock indicator
    if (isMock) {
        setTimeout(() => {
            const resp = document.createElement('div');
            resp.className = 'terminal-row text-success';
            if (command === 'help') {
                resp.textContent = '[Server Help] Available mock commands: status, save, help';
            } else if (command === 'save') {
                resp.textContent = '[Server] Autosave triggered. Map saved successfully.';
            } else {
                resp.textContent = `[Server] console executed: ${command}`;
            }
            el.terminalOutput.appendChild(resp);
            if (el.terminalAutoscroll.checked) {
                el.terminalOutput.scrollTop = el.terminalOutput.scrollHeight;
            }
        }, 300);
    }
    
    try {
        await apiFetch(`/api/servers/${state.selectedConsoleServer}/console/send`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ command })
        });
    } catch (e) {
        console.error('Failed to send console command:', e);
    }
}

function populateSettingsServerSelect() {
    const current = el.settingsServerSelect.value;
    el.settingsServerSelect.innerHTML = '<option value="">-- Server wählen --</option>';
    
    state.servers.forEach(server => {
        const opt = document.createElement('option');
        opt.value = server.id;
        opt.textContent = `${server.name} (${server.user})`;
        el.settingsServerSelect.appendChild(opt);
    });
    
    if (current && state.servers.find(s => s.id === current)) {
        el.settingsServerSelect.value = current;
        el.settingsToolsArea.classList.remove('hidden');
    } else {
        el.settingsToolsArea.classList.add('hidden');
    }
}

function handleSettingsServerChange() {
    const serverID = el.settingsServerSelect.value;
    if (!serverID) {
        el.settingsToolsArea.classList.add('hidden');
        return;
    }
    
    const server = state.servers.find(s => s.id === serverID);
    if (!server) return;
    
    // 1. Generate Systemd template
    const systemdCode = `[Unit]
Description=LinuxGSM ${server.name} Server
After=network.target

[Service]
Type=simple
User=${server.user}
WorkingDirectory=/home/${server.user}
ExecStart=/home/${server.user}/${server.script} start
ExecStop=/home/${server.user}/${server.script} stop
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target`;

    // 2. Generate Cronjob template
    const cronCode = `# LinuxGSM ${server.name} Geplante Wartungsaufgaben
# 1. Server auf Abstürze überwachen (alle 5 Minuten)
*/5 * * * * /home/${server.user}/${server.script} monitor > /dev/null 2>&1

# 2. Automatische Updateprüfung (alle 30 Minuten, startet bei Update neu)
*/30 * * * * /home/${server.user}/${server.script} update > /dev/null 2>&1

# 3. Täglicher Updatecheck & Neustart um 04:30 Uhr nachts
30 4 * * * /home/${server.user}/${server.script} force-update > /dev/null 2>&1

# 4. Wöchentliches Update von LinuxGSM selbst (jeden Sonntag um 00:00 Uhr)
0 0 * * 0 /home/${server.user}/${server.script} update-lgsm > /dev/null 2>&1`;

    el.templateSystemd.textContent = systemdCode;
    el.templateCron.textContent = cronCode;
    el.settingsToolsArea.classList.remove('hidden');
}

// -------------------------------------------------------------
// Config Editor Mode & Parsing Helpers
// -------------------------------------------------------------

function switchConfigMode(mode) {
    state.configMode = mode;
    
    if (mode === 'form') {
        el.btnConfigForm.classList.add('active');
        el.btnConfigRaw.classList.remove('active');
        
        el.editorFormWrapper.classList.remove('hidden');
        el.editorTextWrapper.classList.add('hidden');
        
        // Rebuild form with the latest textarea text in case they modified it in raw view
        const currentText = el.editorTextarea.value;
        const parsed = parseConfig(currentText, state.activeConfigFile);
        if (parsed.length > 0) {
            state.parsedConfigItems = parsed;
            buildConfigForm(parsed);
        }
    } else {
        el.btnConfigForm.classList.remove('active');
        el.btnConfigRaw.classList.add('active');
        
        el.editorFormWrapper.classList.add('hidden');
        el.editorTextWrapper.classList.remove('hidden');
        
        // Serialize form values back to text in case they modified them in the form
        const formValues = {};
        state.parsedConfigItems.forEach(item => {
            const inputEl = document.getElementById(`config-form-val-${item.key}`);
            if (inputEl) {
                if (item.type === 'boolean') {
                    formValues[item.key] = inputEl.checked ? 'true' : 'false';
                } else {
                    formValues[item.key] = inputEl.value;
                }
            }
        });
        
        const ext = state.activeConfigFile.split('.').pop().toLowerCase();
        let content = '';
        if (ext === 'json') {
            content = serializeJsonForm(state.activeConfigContent, formValues);
        } else {
            content = serializeConfigForm(state.activeConfigContent, formValues);
        }
        
        el.editorTextarea.value = content;
        updateEditorLineNumbers();
    }
}

function parseConfig(content, path) {
    const ext = path.split('.').pop().toLowerCase();
    const items = [];
    
    if (ext === 'json') {
        try {
            const obj = JSON.parse(content);
            for (let key in obj) {
                if (typeof obj[key] !== 'object' && obj[key] !== null) {
                    let type = 'text';
                    if (typeof obj[key] === 'boolean') type = 'boolean';
                    else if (typeof obj[key] === 'number') type = 'number';
                    
                    items.push({
                        key: key,
                        value: obj[key],
                        type: type,
                        comments: ''
                    });
                }
            }
            return items;
        } catch (e) {
            return [];
        }
    }
    
    const lines = content.split('\n');
    const kvRegex = /^\s*([\w\.\-]+)\s*([=:])\s*(.*)$/;
    let accumulatedComments = [];
    
    for (let line of lines) {
        const trimmed = line.trim();
        if (trimmed.startsWith('#') || trimmed.startsWith('//') || trimmed.startsWith(';')) {
            const cleanComment = trimmed.replace(/^[#\/;*\s]+/, '');
            if (cleanComment.length > 0) {
                accumulatedComments.push(cleanComment);
            }
            continue;
        }
        
        if (trimmed === '') {
            accumulatedComments = [];
            continue;
        }
        
        const match = line.match(kvRegex);
        if (match) {
            const key = match[1];
            let val = match[3].trim();
            
            if ((val.startsWith('"') && val.endsWith('"')) || (val.startsWith("'") && val.endsWith("'"))) {
                val = val.substring(1, val.length - 1);
            }
            
            let type = 'text';
            let parsedVal = val;
            
            if (val.toLowerCase() === 'true' || val.toLowerCase() === 'false') {
                type = 'boolean';
                parsedVal = val.toLowerCase() === 'true';
            } else if (/^\d+$/.test(val)) {
                type = 'number';
                parsedVal = parseInt(val, 10);
            } else if (/^\d+\.\d+$/.test(val)) {
                type = 'number';
                parsedVal = parseFloat(val);
            }
            
            items.push({
                key: key,
                value: parsedVal,
                type: type,
                comments: accumulatedComments.join(' ')
            });
            
            accumulatedComments = [];
        }
    }
    
    return items;
}

function buildConfigForm(items) {
    el.configFormBuilder.innerHTML = '';
    
    items.forEach(item => {
        const formItem = document.createElement('div');
        formItem.className = 'config-form-item';
        
        const left = document.createElement('div');
        left.className = 'config-form-item-left';
        
        let prettyLabel = item.key
            .replace(/[-_]/g, ' ')
            .replace(/\b\w/g, c => c.toUpperCase());
            
        const labelEl = document.createElement('div');
        labelEl.className = 'config-form-label';
        labelEl.textContent = prettyLabel;
        left.appendChild(labelEl);
        
        const keyEl = document.createElement('div');
        keyEl.className = 'config-form-key';
        keyEl.textContent = item.key;
        left.appendChild(keyEl);
        
        if (item.comments) {
            const descEl = document.createElement('div');
            descEl.className = 'config-form-description';
            descEl.textContent = item.comments;
            left.appendChild(descEl);
        }
        
        formItem.appendChild(left);
        
        const container = document.createElement('div');
        container.className = 'config-form-input-container';
        
        if (item.type === 'boolean') {
            const switchEl = document.createElement('label');
            switchEl.className = 'switch';
            
            const check = document.createElement('input');
            check.type = 'checkbox';
            check.id = `config-form-val-${item.key}`;
            check.checked = item.value;
            switchEl.appendChild(check);
            
            const slider = document.createElement('span');
            slider.className = 'slider';
            switchEl.appendChild(slider);
            
            container.appendChild(switchEl);
        } else if (item.type === 'number') {
            const input = document.createElement('input');
            input.type = 'number';
            input.id = `config-form-val-${item.key}`;
            input.className = 'form-control form-control-sm';
            input.style.width = '120px';
            input.value = item.value;
            container.appendChild(input);
        } else {
            const input = document.createElement('input');
            input.type = 'text';
            input.id = `config-form-val-${item.key}`;
            input.className = 'form-control form-control-sm';
            input.style.minWidth = '280px';
            input.value = item.value;
            container.appendChild(input);
        }
        
        formItem.appendChild(container);
        el.configFormBuilder.appendChild(formItem);
    });
}

function serializeConfigForm(originalContent, formValues) {
    const lines = originalContent.split('\n');
    const result = [];
    const kvRegex = /^\s*([\w\.\-]+)\s*([=:])\s*(.*)$/;
    
    for (let line of lines) {
        const match = line.match(kvRegex);
        if (match) {
            const key = match[1];
            const delimiter = match[2];
            const rawVal = match[3].trim();
            
            if (formValues.hasOwnProperty(key)) {
                let newVal = formValues[key];
                
                const isDoubleQuoted = rawVal.startsWith('"') && rawVal.endsWith('"');
                const isSingleQuoted = rawVal.startsWith("'") && rawVal.endsWith("'");
                
                const indent = line.substring(0, line.indexOf(key));
                
                if (isDoubleQuoted) {
                    result.push(`${indent}${key}${delimiter}"${newVal}"`);
                } else if (isSingleQuoted) {
                    result.push(`${indent}${key}${delimiter}'${newVal}'`);
                } else {
                    result.push(`${indent}${key}${delimiter}${newVal}`);
                }
                continue;
            }
        }
        result.push(line);
    }
    
    return result.join('\n');
}

function serializeJsonForm(originalContent, formValues) {
    try {
        const obj = JSON.parse(originalContent);
        for (let key in formValues) {
            if (obj.hasOwnProperty(key)) {
                let val = formValues[key];
                if (val === 'true') val = true;
                else if (val === 'false') val = false;
                else if (!isNaN(val) && val !== '') {
                    val = Number(val);
                }
                obj[key] = val;
            }
        }
        return JSON.stringify(obj, null, 2);
    } catch (e) {
        return originalContent;
    }
}

// -------------------------------------------------------------
// Theme & Language (i18n) Switchers
// -------------------------------------------------------------

const i18n = {
    en: {
        "menu-dashboard": "Dashboard",
        "menu-installer": "Game Installer",
        "menu-overview": "Overview",
        "menu-console": "Live Console",
        "menu-settings": "Settings",
        "menu-logout": "Logout",
        "login-title": "LinuxGSM WebAdmin",
        "login-subtitle": "Sign in to manage your game servers.",
        "username-label": "Username",
        "password-label": "Password",
        "btn-login": "Login",
        "dash-title": "Server Dashboard",
        "dash-subtitle": "Manage your active LinuxGSM game server instances.",
        "dash-search-placeholder": "Search servers...",
        "btn-all": "All",
        "btn-online": "Online",
        "btn-offline": "Offline",
        "btn-action": "Actions",
        "btn-details": "Details",
        "btn-backup": "Backup",
        "btn-validate": "Validate",
        "btn-update-lgsm": "Update LGSM",
        "btn-refresh": "Refresh",
        "inst-title": "Game Installer",
        "inst-subtitle": "Install a new LinuxGSM server. A system user will be automatically created.",
        "inst-btn-sync": "Update Game List",
        "inst-search-placeholder": "Search games...",
        "inst-user-label": "System Username",
        "inst-pass-label": "User Password (Optional)",
        "inst-btn-install": "Install Game Server",
        "settings-title": "Settings & Info",
        "settings-subtitle": "Change administrative credentials and view system templates.",
        "settings-pass-card": "Change Administrator Password",
        "settings-new-pass": "New Password",
        "settings-confirm-pass": "Confirm New Password",
        "settings-btn-save": "Update Password",
        "settings-info-card": "Host System Information",
        "settings-templates-card": "Automation Templates",
        "settings-btn-systemd": "Generate Systemd Service",
        "settings-btn-cron": "Generate Cronjobs",
        "overview-title": "Overview & Monitoring",
        "overview-subtitle": "Resource utilization of the host system and active game servers.",
        "overview-cpu": "Host CPU Usage",
        "overview-ram": "Memory (RAM)",
        "settings-old-pass-label": "Current Password",
        "settings-new-pass-label": "New Password",
        "settings-confirm-pass-label": "Confirm New Password",
        "settings-pass-card-desc": "Change the web dashboard password.",
        "settings-tools-card": "Server-specific Tools",
        "settings-tools-card-desc": "Select one of your active servers to show copy-paste templates for systemd and cronjobs.",
        "settings-tools-select-label": "Select server",
        "settings-tools-select-default": "-- Select server --",
        "settings-info-os": "Operating System",
        "settings-info-mode": "Dashboard Mode",
        "modal-config-no-file": "Choose a file",
        "config-no-files": "No config files found",
        "username-placeholder": "admin",
        "password-placeholder": "Enter password",
        "console-select-default": "-- Select a server --",
        "console-no-server-selected": "No server selected",
        "console-mode-log": "Console Log File",
        "console-empty-prompt": "Select a server above to load the console.",
        "console-input-placeholder": "Send command to server console...",
        "btn-send": "Send",
        "overview-disk": "Disk Space",
        "overview-history": "Resource History (Last 5 Minutes)",
        "overview-table": "Resource Usage per Server",
        "overview-col-server": "Server",
        "overview-col-user": "User",
        "overview-col-status": "Status",
        "overview-col-cpu": "CPU Usage",
        "overview-col-ram": "RAM Usage",
        "overview-col-pids": "Processes (PIDs)",
        "modal-stream-running": "Running",
        "modal-stream-autoscroll": "Auto-Scroll",
        "modal-stream-close": "Close",
        "modal-config-title": "Configuration Editor",
        "modal-config-files": "Files",
        "modal-config-no-file": "No file selected",
        "modal-config-tab-form": "Form View",
        "modal-config-tab-raw": "Raw Text",
        "modal-config-btn-save": "Save Config",
        "modal-config-placeholder": "Select a configuration file from the list on the left to edit it.",
        "modal-config-select-prompt": "Select a configuration file from the list on the left.",
        "modal-config-default-warning": "Note: LinuxGSM overwrites _default.cfg during updates. Please edit the config or common.cfg instead.",
        "inst-user-placeholder": "e.g. valheimserver",
        "inst-user-help": "Allowed: lowercase letters, numbers, and underscores (3-16 chars). The user will be created on the host.",
        "inst-pass-placeholder": "Leave blank for random password",
        "inst-pass-help": "Password for SSH/system login. Not required by LinuxGSM itself.",
        "inst-warning-text": "The installation downloads SteamCMD and all game files. This can take 5-30 minutes depending on your internet connection.",
        "card-port": "Port",
        "status-offline": "Offline",
        "status-online": "Online",
        "status-installing": "Installing...",
        "status-updating": "Updating...",
        "btn-start": "Start",
        "btn-stop": "Stop",
        "btn-restart": "Restart",
        "btn-console": "Console",
        "btn-configs": "Configs",
        "btn-update": "Update",
        "btn-backup": "Backup",
        "btn-validate": "Validate",
        "overview-cores": "Cores",
        "overview-clock": "Clock",
        "overview-used": "Used",
        "overview-free": "Free",
        "overview-no-active-processes": "No active server processes found.",
        "no-servers-found": "No game servers found.",
        "terminal-loading": "Loading console...",
        "console-title": "Live Console & Logs",
        "console-subtitle": "View the tmux live screen or log files of your game server.",
        "config-loading-files": "Loading files...",
        "config-loading-file": "Loading content...",
        "config-load-error": "Load error",
        "config-load-file-error": "Error loading file.",
        "saving": "Saving...",
        "saved": "Saved!",
        "config-save-error": "Error saving configuration.",
        "settings-pass-mismatch": "New passwords do not match.",
        "settings-pass-success": "Password changed successfully.",
        "settings-pass-error": "Error changing password.",
        "connection-error": "Connection error.",
        "label-script": "Script:",
        "games-sync-start-error": "Error starting sync",
        "games-sync-success": "Successfully synced!",
        "games-sync-initializing": "Initializing crawler...",
        "overview-none": "None",
        "settings-mode-mock": "DEMO MODE (Windows Mock)",
        "settings-mode-prod": "PRODUCTION (Linux Host)",
        "status-connecting": "Connecting...",
        "status-success": "Success",
        "status-error": "Error",
        "status-cancelled": "Cancelled",
        "status-creating-user": "Creating user...",
        "status-installing-action": "Installing...",
        "status-running-action": "Executing...",
        "modal-stream-title-run": "Executing {action} for {server}",
        "modal-stream-title-install": "Installing {game}",
        "modal-stream-err-run": "\n[ERROR] Action stream connection interrupted.",
        "modal-stream-err-install": "\n[ERROR] Installation stream connection interrupted.",
        "btn-portcheck": "Test Ports",
        "modal-portcheck-title": "Verify Port Reachability",
        "modal-portcheck-scanning": "Testing reachability from the outside...",
        "modal-portcheck-public-ip": "Public IP Address:",
        "modal-portcheck-th-port": "Port",
        "modal-portcheck-th-proto": "Protocol",
        "modal-portcheck-th-status": "Status",
        "modal-portcheck-help-note": "Note: If a port is shown as closed, ensure your server is running and port forwarding rules are correctly configured in both your router and system firewall.",
        "modal-portcheck-close-btn": "Close",
        "btn-delete-server": "Delete",
        "modal-delete-title": "Delete Game Server",
        "modal-delete-warning": "WARNING: This action is permanent and cannot be undone! It will permanently delete all game files, configs, and the dedicated system user from this server.",
        "modal-delete-prompt-label": "Please type the name of the server to confirm deletion:",
        "modal-delete-cancel-btn": "Cancel",
        "modal-delete-submit-btn": "Delete Server Permanently"
        ,
        "console-no-server-connected": "No server connected"
    },
    de: {
        "menu-dashboard": "Dashboard",
        "menu-installer": "Spiele-Installer",
        "menu-overview": "Übersicht",
        "menu-console": "Live-Konsole",
        "menu-settings": "Einstellungen",
        "menu-logout": "Abmelden",
        "login-title": "LinuxGSM WebAdmin",
        "login-subtitle": "Melde dich an, um deine Gameserver zu verwalten.",
        "username-label": "Benutzername",
        "password-label": "Passwort",
        "btn-login": "Anmelden",
        "dash-title": "Server Dashboard",
        "dash-subtitle": "Verwalte deine aktiven LinuxGSM Game Server Instanzen.",
        "dash-search-placeholder": "Server filtern...",
        "btn-all": "Alle",
        "btn-online": "Online",
        "btn-offline": "Offline",
        "btn-action": "Aktionen",
        "btn-details": "Details",
        "btn-backup": "Backup",
        "btn-validate": "Validieren",
        "btn-update-lgsm": "LGSM Updaten",
        "btn-refresh": "Aktualisieren",
        "inst-title": "Spiele-Installer",
        "inst-subtitle": "Installiere einen neuen LinuxGSM Server. Es wird automatisch ein neuer Systembenutzer angelegt.",
        "inst-btn-sync": "Spieleliste aktualisieren",
        "inst-search-placeholder": "Spiele durchsuchen...",
        "inst-user-label": "System-Benutzername",
        "inst-pass-label": "Passwort (Optional)",
        "inst-btn-install": "Gameserver installieren",
        "settings-title": "Einstellungen & Info",
        "settings-subtitle": "Ändere Administrator-Anmeldedaten und kopiere Automatisierungs-Templates.",
        "settings-pass-card": "Dashboard-Zugangsdaten",
        "settings-pass-card-desc": "Ändere das Passwort für das Web-Dashboard.",
        "settings-old-pass-label": "Aktuelles Passwort",
        "settings-new-pass-label": "Neues Passwort",
        "settings-confirm-pass-label": "Neues Passwort bestätigen",
        "settings-btn-save": "Passwort speichern",
        "settings-info-card": "System-Informationen",
        "settings-info-version": "Dashboard Version",
        "settings-info-os": "Betriebssystem",
        "settings-info-pid": "Prozess-ID (PID)",
        "settings-info-mode": "Dashboard Modus",
        "settings-tools-card": "Server-Spezifische Hilfsmittel",
        "settings-tools-card-desc": "Wähle einen deiner aktiver Server, um copy-paste Vorlagen für Autostart und geplante Aufgaben (Cronjobs) anzuzeigen.",
        "settings-tools-select-label": "Server auswählen",
        "settings-tools-select-default": "-- Server wählen --",
        "settings-tools-systemd-title": "Systemd-Dienst (Autostart bei Boot)",
        "settings-tools-systemd-desc": "Erstelle eine Datei unter /etc/systemd/system/lgsm-[server].service als root und füge folgenden Inhalt ein:",
        "settings-tools-cron-title": "Cronjobs (Geplante Wartungsaufgaben)",
        "settings-tools-cron-desc": "Füge diese Zeilen in die Crontab des Serverbenutzers (oder über crontab -e als root) ein:",
        "overview-title": "Übersicht & Monitoring",
        "overview-subtitle": "Ressourcen-Auslastung des Host-Systems und der einzelnen Gameserver-Instanzen.",
        "overview-cpu": "CPU Auslastung",
        "overview-ram": "Arbeitsspeicher (RAM)",
        "username-placeholder": "admin",
        "password-placeholder": "Passwort eingeben",
        "console-select-default": "-- Wähle einen Server --",
        "console-no-server-selected": "Kein Server gewählt",
        "console-mode-log": "Konsolen-Logdatei",
        "console-empty-prompt": "Wähle oben einen Server aus, um die Konsole zu laden.",
        "console-input-placeholder": "Befehl an die Serverkonsole senden...",
        "btn-send": "Senden",
        "overview-disk": "Festplatte",
        "overview-history": "Ressourcenverlauf (Letzte 5 Minuten)",
        "overview-table": "Ressourcennutzung pro Server",
        "overview-col-server": "Server",
        "overview-col-user": "Benutzer",
        "overview-col-status": "Status",
        "overview-col-cpu": "CPU Auslastung",
        "overview-col-ram": "RAM Auslastung",
        "overview-col-pids": "Prozesse (PIDs)",
        "modal-stream-running": "Laufend",
        "modal-stream-autoscroll": "Auto-Scroll",
        "modal-stream-close": "Schließen",
        "modal-config-title": "Konfigurations-Editor",
        "modal-config-files": "Dateien",
        "modal-config-no-file": "Keine Datei ausgewählt",
        "config-no-files": "Keine Configs gefunden",
        "password-placeholder": "Passwort eingeben",
        "btn-send": "Senden",
        "console-empty-prompt": "Wähle oben einen Server aus, um die Konsole zu laden.",
        "console-input-placeholder": "Befehl an die Serverkonsole senden...",
        "console-mode-log": "Konsolen-Logdatei",
        "console-no-server-selected": "Kein Server gewählt",
        "console-no-server-connected": "Kein Server verbunden",
        "console-title": "Live-Konsole & Protokoll",
        "console-subtitle": "Sieh den Live-Bildschirm (tmux) oder Logdateien deines Gameservers ein.",
        "settings-btn-save": "Passwort speichern",
        "modal-config-btn-save": "Speichern",
        "config-save-error": "Fehler beim Speichern der Konfiguration.",
        "terminal-loading": "Lade Konsole...",
        "config-loading-file": "Lade Inhalt...",
        "modal-config-tab-form": "Formular",
        "modal-config-tab-raw": "Text",
        "modal-config-btn-save": "Speichern",
        "modal-config-placeholder": "Wähle eine Konfigurationsdatei aus der Liste links aus, um sie zu bearbeiten.",
        "modal-config-select-prompt": "Wähle eine Konfigurationsdatei aus der Liste links aus.",
        "modal-config-default-warning": "Hinweis: LinuxGSM überschreibt _default.cfg bei Updates. Editiere stattdessen die datei.cfg oder common.cfg.",
        "inst-user-placeholder": "z.B. valheimserver",
        "inst-user-help": "Erlaubt sind Kleinbuchstaben, Zahlen und Unterstriche (3-16 Zeichen). Der Benutzer wird auf dem Server angelegt.",
        "inst-pass-placeholder": "Frei lassen für zufälliges Passwort",
        "inst-pass-help": "Passwort für den SSH/System-Login des Benutzers. Wird für LinuxGSM selbst nicht benötigt.",
        "inst-warning-text": "Die Installation lädt SteamCMD und alle Spieldateien herunter. Dies kann je nach Spiel und Internetleitung 5-30 Minuten dauern.",
        "card-port": "Port",
        "status-offline": "Offline",
        "status-online": "Online",
        "status-installing": "Wird installiert...",
        "status-updating": "Wird aktualisiert...",
        "btn-start": "Start",
        "btn-stop": "Stop",
        "btn-restart": "Neustart",
        "btn-console": "Konsole",
        "btn-configs": "Configs",
        "btn-update": "Update",
        "btn-backup": "Backup",
        "btn-validate": "Validieren",
        "overview-cores": "Kerne",
        "overview-clock": "Takt",
        "overview-used": "Genutzt",
        "overview-free": "Frei",
        "overview-no-active-processes": "Keine aktiven Serverprozesse gefunden.",
        "no-servers-found": "Keine Gameserver gefunden.",
        "terminal-loading": "Lade Konsole...",
        "config-loading-files": "Lade Dateien...",
        "config-loading-file": "Lade Inhalt...",
        "config-load-error": "Ladefehler",
        "config-load-file-error": "Fehler beim Laden der Datei.",
        "saving": "Speichert...",
        "saved": "Gespeichert!",
        "config-save-error": "Fehler beim Speichern der Konfiguration.",
        "settings-pass-mismatch": "Die neuen Passwörter stimmen nicht überein.",
        "settings-pass-success": "Passwort erfolgreich geändert.",
        "settings-pass-error": "Fehler beim Ändern des Passworts.",
        "connection-error": "Verbindungsfehler.",
        "label-script": "Skript:",
        "games-sync-start-error": "Fehler beim Starten",
        "games-sync-success": "Erfolgreich synchronisiert!",
        "games-sync-initializing": "Initialisiere Crawler...",
        "overview-none": "Keine",
        "settings-mode-mock": "MOCK / DEMO MODUS (Windows)",
        "settings-mode-prod": "PRODUKTION (Linux Root)",
        "status-connecting": "Verbinde...",
        "status-success": "Erfolgreich",
        "status-error": "Fehler",
        "status-cancelled": "Abgebrochen",
        "status-creating-user": "Erstelle Benutzer...",
        "status-installing-action": "Installiere...",
        "status-running-action": "Ausführen",
        "modal-stream-title-run": "{action} wird ausgeführt für {server}",
        "modal-stream-title-install": "{game} wird installiert",
        "modal-stream-err-run": "\n[FEHLER] Stream Verbindung abgebrochen.",
        "modal-stream-err-install": "\n[FEHLER] Installations-Stream abgebrochen.",
        "btn-portcheck": "Ports prüfen",
        "modal-portcheck-title": "Port-Erreichbarkeit prüfen",
        "modal-portcheck-scanning": "Prüfe Erreichbarkeit von außen...",
        "modal-portcheck-public-ip": "Öffentliche IP-Adresse:",
        "modal-portcheck-th-port": "Port",
        "modal-portcheck-th-proto": "Protokoll",
        "modal-portcheck-th-status": "Status",
        "modal-portcheck-help-note": "Hinweis: Wenn ein Port als geschlossen angezeigt wird, stelle sicher, dass der Server läuft und die Port-Freigaben in deinem Router sowie in der Firewall korrekt eingerichtet sind.",
        "modal-portcheck-close-btn": "Schließen",
        "btn-delete-server": "Löschen",
        "modal-delete-title": "Gameserver löschen",
        "modal-delete-warning": "WARNUNG: Diese Aktion kann nicht rückgängig gemacht werden! Dadurch werden alle Spieldateien, Konfigurationen und (falls exklusiv genutzt) der gesamte Systembenutzer dauerhaft vom Server gelöscht.",
        "modal-delete-prompt-label": "Bitte gebe den Namen des Servers ein, um das Löschen zu bestätigen:",
        "modal-delete-cancel-btn": "Abbrechen",
        "modal-delete-submit-btn": "Server unwiderruflich löschen"
        ,
        "console-no-server-connected": "Kein Server verbunden"
    }
};

function t(key) {
    return (i18n[state.language] && i18n[state.language][key]) || key;
}

function initTheme() {
    const savedTheme = localStorage.getItem('theme') || 'dark';
    setTheme(savedTheme);
}

function setTheme(theme) {
    state.theme = theme;
    localStorage.setItem('theme', theme);
    
    if (theme === 'light') {
        document.body.classList.add('light-theme');
        el.themeIconSun.classList.add('hidden');
        el.themeIconMoon.classList.remove('hidden');
    } else {
        document.body.classList.remove('light-theme');
        el.themeIconSun.classList.remove('hidden');
        el.themeIconMoon.classList.add('hidden');
    }
}

function toggleTheme() {
    setTheme(state.theme === 'dark' ? 'light' : 'dark');
}

// Default set to English ('en')
function initLanguage() {
    const savedLang = localStorage.getItem('language') || 'en';
    applyLanguage(savedLang);
}

function toggleLanguage() {
    const nextLang = state.language === 'en' ? 'de' : 'en';
    applyLanguage(nextLang);
}

function applyLanguage(lang) {
    state.language = lang;
    localStorage.setItem('language', lang);
    el.langToggleText.textContent = lang.toUpperCase();
    
    document.querySelectorAll('[data-i18n]').forEach(element => {
        const key = element.getAttribute('data-i18n');
        if (i18n[lang] && i18n[lang][key]) {
            element.textContent = i18n[lang][key];
        }
    });
    
    document.querySelectorAll('[data-i18n-placeholder]').forEach(element => {
        const key = element.getAttribute('data-i18n-placeholder');
        if (i18n[lang] && i18n[lang][key]) {
            element.placeholder = i18n[lang][key];
        }
    });
    
    // Re-render all dynamic components in the active language
    if (state.servers && state.servers.length > 0) {
        renderServersGrid();
    }
    if (state.lastMetricsData) {
        renderMetricsDetails(state.lastMetricsData);
    }
    if (state.lastServersMetrics) {
        renderMonitorServerTable(state.lastServersMetrics);
    }
    if (state.lastSettingsData) {
        renderSettingsMode(state.lastSettingsData);
    }
}

async function loadGamesList() {
    try {
        const res = await apiFetch('/api/games/list');
        const data = await res.json();
        state.games = data;
        renderGamesList();
        
        // Check if backend is currently running a startup/background sync
        const statusRes = await apiFetch('/api/games/sync/status');
        const statusData = await statusRes.json();
        if (statusData.status === 'syncing') {
            pollSyncProgress();
        }
    } catch (e) {
        console.error('Failed to load games list:', e);
        renderGamesList();
    }
}

function pollSyncProgress() {
    el.btnSyncGames.disabled = true;
    el.gamesSyncProgressBar.classList.remove('hidden');
    
    // Avoid spawning multiple poll timers if one is already active
    if (window.syncPollInterval) {
        clearInterval(window.syncPollInterval);
    }
    
    window.syncPollInterval = setInterval(async () => {
        try {
            const statusRes = await apiFetch('/api/games/sync/status');
            const statusData = await statusRes.json();
            
            if (statusData.status === 'syncing') {
                el.gamesSyncProgressText.textContent = statusData.progress;
                
                // Match "Processing games (42 discovered so far)..."
                const match = statusData.progress.match(/Processing games \((\d+)\s+discovered/i);
                if (match) {
                    const parsed = parseInt(match[1], 10);
                    const percent = Math.min(95, Math.floor((parsed / 139) * 90) + 5);
                    el.gamesSyncProgressFill.style.width = `${percent}%`;
                } else {
                    el.gamesSyncProgressFill.style.width = '10%';
                }
            } else {
                clearInterval(window.syncPollInterval);
                window.syncPollInterval = null;
                el.gamesSyncProgressFill.style.width = '100%';
                el.gamesSyncProgressText.textContent = t('games-sync-success');
                
                // Fetch the newly updated list
                try {
                    const res = await apiFetch('/api/games/list');
                    const data = await res.json();
                    state.games = data;
                    renderGamesList();
                } catch (e) {}
                
                setTimeout(() => {
                    el.gamesSyncProgressBar.classList.add('hidden');
                    el.btnSyncGames.disabled = false;
                }, 2000);
            }
        } catch (err) {
            clearInterval(window.syncPollInterval);
            window.syncPollInterval = null;
            el.btnSyncGames.disabled = false;
        }
    }, 1000);
}

async function syncGamesList() {
    el.btnSyncGames.disabled = true;
    el.gamesSyncProgressBar.classList.remove('hidden');
    el.gamesSyncProgressFill.style.width = '5%';
    el.gamesSyncProgressText.textContent = t('games-sync-initializing');
    
    try {
        const res = await apiFetch('/api/games/sync');
        if (res.status !== 200) {
            throw new Error('Sync endpoint failed');
        }
        pollSyncProgress();
    } catch (e) {
        el.gamesSyncProgressText.textContent = t('games-sync-start-error');
        el.btnSyncGames.disabled = false;
    }
}

async function checkServerPorts(serverId) {
    el.modalPortcheckSubtitle.textContent = serverId;
    el.portcheckLoading.classList.remove('hidden');
    el.portcheckResults.classList.add('hidden');
    el.modalPortcheck.classList.remove('hidden');

    try {
        const res = await apiFetch(`/api/servers/${serverId}/portcheck`);
        if (res.status !== 200) {
            throw new Error('Portcheck API failed');
        }
        const data = await res.json();

        el.portcheckIp.textContent = data.public_ip;
        el.portcheckTableBody.innerHTML = '';

        data.probes.forEach(probe => {
            const row = document.createElement('tr');
            row.style.borderBottom = '1px solid rgba(255,255,255,0.05)';
            
            const badgeClass = probe.open ? 'badge badge-success' : 'badge badge-danger';
            const statusText = probe.open ? 
                (state.language === 'de' ? 'Offen' : 'Open') : 
                (state.language === 'de' ? 'Geschlossen' : 'Closed');

            row.innerHTML = `
                <td style="padding: 0.75rem 0.5rem; text-align: left; font-family: monospace; font-size: 0.85rem;">
                    ${probe.port} <span style="font-size: 0.7rem; color: var(--text-muted); display: block; margin-top: 2px;">${probe.description}</span>
                </td>
                <td style="padding: 0.75rem 0.5rem; text-align: left; font-size: 0.8rem;">
                    ${probe.protocol}
                </td>
                <td style="padding: 0.75rem 0.5rem; text-align: right;">
                    <span class="${badgeClass}" style="font-size: 0.75rem; padding: 0.2rem 0.5rem; border-radius: 4px;">${statusText}</span>
                </td>
            `;
            el.portcheckTableBody.appendChild(row);
        });

        el.portcheckLoading.classList.add('hidden');
        el.portcheckResults.classList.remove('hidden');
    } catch (err) {
        console.error('Port Check failed:', err);
        el.portcheckLoading.classList.add('hidden');
        alert(state.language === 'de' ? 'Port-Prüfung fehlgeschlagen.' : 'Port check failed.');
        el.modalPortcheck.classList.add('hidden');
    }
}

function openDeleteServerModal(serverId) {
    el.modalDeleteSubtitle.textContent = serverId;
    el.deleteConfirmInput.value = '';
    el.deleteConfirmInput.placeholder = serverId;
    el.btnDeleteConfirmSubmit.disabled = true;
    el.modalDelete.classList.remove('hidden');
    setTimeout(() => el.deleteConfirmInput.focus(), 100);
}

async function confirmDeleteServer() {
    const serverId = el.modalDeleteSubtitle.textContent.trim();
    el.btnDeleteConfirmSubmit.disabled = true;
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/delete`, {
            method: 'POST'
        });
        if (res.status !== 200) {
            throw new Error('Delete API failed');
        }
        el.modalDelete.classList.add('hidden');
        alert(state.language === 'de' ? `Server ${serverId} wurde erfolgreich gelöscht.` : `Server ${serverId} deleted successfully.`);
        refreshDashboard();
    } catch (err) {
        console.error('Failed to delete server:', err);
        alert(state.language === 'de' ? 'Löschen fehlgeschlagen.' : 'Deletion failed.');
        el.btnDeleteConfirmSubmit.disabled = false;
    }
}
