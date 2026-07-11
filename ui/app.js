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
    gamesSyncProgressText: document.getElementById('games-sync-progress-text'),
    
    // Backups View
    backupsServerSelect: document.getElementById('backups-server-select'),
    backupsBtnCreate: document.getElementById('backups-btn-create'),
    backupsTableBody: document.getElementById('backups-list-table-body'),
    backupsEmptyMessage: document.getElementById('backups-empty-message'),
    backupsLoading: document.getElementById('backups-loading'),
    backupsSettingsForm: document.getElementById('backups-settings-form'),
    backupsSettingMaxBackups: document.getElementById('backups-setting-maxbackups'),
    backupsSettingMaxBackupDays: document.getElementById('backups-setting-maxbackupdays'),
    backupsSettingStopOnBackup: document.getElementById('backups-setting-stoponbackup'),
    backupsSettingsSaveBtn: document.getElementById('backups-settings-save-btn'),
    backupsSettingsMessage: document.getElementById('backups-settings-message'),
    backupsSettingAutoBackupEnabled: document.getElementById('backups-setting-autobackup-enabled'),
    backupsSettingCronContainer: document.getElementById('backups-setting-cron-container'),
    backupsSettingCronPreset: document.getElementById('backups-setting-cron-preset'),
    backupsSettingCustomCronContainer: document.getElementById('backups-setting-custom-cron-container'),
    backupsSettingCronCustom: document.getElementById('backups-setting-cron-custom'),
    backupsBtnUpload: document.getElementById('backups-btn-upload'),
    backupsInputUpload: document.getElementById('backups-input-upload'),
    backupsUploadProgressContainer: document.getElementById('backups-upload-progress-container'),
    backupsUploadFilename: document.getElementById('backups-upload-filename'),
    backupsUploadPercentage: document.getElementById('backups-upload-percentage'),
    backupsUploadProgressBar: document.getElementById('backups-upload-progress-bar'),
    
    // Extensions elements
    consoleBtnUpdateLgsm: document.getElementById('console-btn-update-lgsm'),
    consoleBtnForceUpdate: document.getElementById('console-btn-force-update'),
    consoleBtnTestAlert: document.getElementById('console-btn-test-alert'),
    consoleGameActions: document.getElementById('console-game-actions'),
    
    alertsSettingDiscordEnabled: document.getElementById('alerts-setting-discord-enabled'),
    alertsSettingDiscordWebhook: document.getElementById('alerts-setting-discord-webhook'),
    alertsSettingDiscordContainer: document.getElementById('alerts-setting-discord-container'),
    
    alertsSettingTelegramEnabled: document.getElementById('alerts-setting-telegram-enabled'),
    alertsSettingTelegramToken: document.getElementById('alerts-setting-telegram-token'),
    alertsSettingTelegramChatid: document.getElementById('alerts-setting-telegram-chatid'),
    alertsSettingTelegramContainer: document.getElementById('alerts-setting-telegram-container'),
    
    alertsSettingEmailEnabled: document.getElementById('alerts-setting-email-enabled'),
    alertsSettingEmailSmtp: document.getElementById('alerts-setting-email-smtp'),
    alertsSettingEmailPort: document.getElementById('alerts-setting-email-port'),
    alertsSettingEmailUser: document.getElementById('alerts-setting-email-user'),
    alertsSettingEmailPass: document.getElementById('alerts-setting-email-pass'),
    alertsSettingEmailDest: document.getElementById('alerts-setting-email-dest'),
    alertsSettingEmailContainer: document.getElementById('alerts-setting-email-container'),
    
    alertsSettingsPanel: document.getElementById('alerts-settings-panel'),
    alertsSettingsSaveBtn: document.getElementById('alerts-settings-save-btn'),
    alertsSettingsMessage: document.getElementById('alerts-settings-message'),
    alertsSettingsForm: document.getElementById('alerts-settings-form'),
    
    settingsToolsBtnSystemd: document.getElementById('settings-tools-btn-systemd'),
    settingsToolsBtnCron: document.getElementById('settings-tools-btn-cron')
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
    el.consoleBtnUpdateLgsm.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'update-lgsm'));
    el.consoleBtnForceUpdate.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'force-update'));
    el.consoleBtnTestAlert.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'test-alert'));
    
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
    el.settingsToolsBtnSystemd.addEventListener('click', installSystemdService);
    el.settingsToolsBtnCron.addEventListener('click', installCronjobs);
    
    // Backups listeners
    el.backupsServerSelect.addEventListener('change', handleBackupsServerChange);
    el.backupsBtnCreate.addEventListener('click', handleBackupsBtnCreateClick);
    el.backupsBtnUpload.addEventListener('click', () => el.backupsInputUpload.click());
    el.backupsInputUpload.addEventListener('change', handleBackupsUpload);
    el.backupsSettingsForm.addEventListener('submit', saveBackupSettings);
    
    // Alerts listeners
    el.alertsSettingsForm.addEventListener('submit', saveAlertSettings);
    el.alertsSettingDiscordEnabled.addEventListener('change', () => toggleContainer(el.alertsSettingDiscordEnabled, el.alertsSettingDiscordContainer));
    el.alertsSettingTelegramEnabled.addEventListener('change', () => toggleContainer(el.alertsSettingTelegramEnabled, el.alertsSettingTelegramContainer));
    el.alertsSettingEmailEnabled.addEventListener('change', () => toggleContainer(el.alertsSettingEmailEnabled, el.alertsSettingEmailContainer));
    
    el.backupsSettingAutoBackupEnabled.addEventListener('change', () => {
        if (el.backupsSettingAutoBackupEnabled.checked) {
            el.backupsSettingCronContainer.classList.remove('hidden');
        } else {
            el.backupsSettingCronContainer.classList.add('hidden');
        }
    });
    el.backupsSettingCronPreset.addEventListener('change', () => {
        if (el.backupsSettingCronPreset.value === 'custom') {
            el.backupsSettingCustomCronContainer.classList.remove('hidden');
        } else {
            el.backupsSettingCustomCronContainer.classList.add('hidden');
        }
    });
    
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

    // Mobile navigation listeners
    const btnMobileToggle = document.getElementById('btn-mobile-toggle');
    const sidebarBackdrop = document.getElementById('sidebar-backdrop');
    const appSidebar = document.getElementById('app-sidebar');
    const btnMobileThemeToggle = document.getElementById('btn-mobile-theme-toggle');

    if (btnMobileToggle && sidebarBackdrop && appSidebar) {
        btnMobileToggle.addEventListener('click', () => {
            appSidebar.classList.add('active');
            sidebarBackdrop.classList.add('active');
        });

        sidebarBackdrop.addEventListener('click', () => {
            appSidebar.classList.remove('active');
            sidebarBackdrop.classList.remove('active');
        });

        // Close sidebar drawer on clicking any menu item
        const menuItems = appSidebar.querySelectorAll('.menu-item');
        menuItems.forEach(item => {
            item.addEventListener('click', () => {
                appSidebar.classList.remove('active');
                sidebarBackdrop.classList.remove('active');
            });
        });
    }

    if (btnMobileThemeToggle) {
        btnMobileThemeToggle.addEventListener('click', toggleTheme);
    }

    // User modal listeners
    const btnCreateUserModal = document.getElementById('btn-create-user-modal');
    if (btnCreateUserModal) btnCreateUserModal.addEventListener('click', openCreateUserModal);
    
    const modalUserCloseX = document.getElementById('modal-user-close-x');
    if (modalUserCloseX) modalUserCloseX.addEventListener('click', () => document.getElementById('modal-user').classList.add('hidden'));
    
    const modalUserClose = document.getElementById('modal-user-close');
    if (modalUserClose) modalUserClose.addEventListener('click', () => document.getElementById('modal-user').classList.add('hidden'));
    
    const modalUserSubmit = document.getElementById('modal-user-submit');
    if (modalUserSubmit) modalUserSubmit.addEventListener('click', saveUserForm);
    
    const userRoleSelect = document.getElementById('user-role');
    if (userRoleSelect) userRoleSelect.addEventListener('change', updateUserFormVisibility);
    
    // Dashboard updater listeners
    const btnCheckUpdate = document.getElementById('btn-check-update');
    if (btnCheckUpdate) btnCheckUpdate.addEventListener('click', () => checkDashboardUpdate(true));
    
    const btnTriggerUpdate = document.getElementById('btn-trigger-update');
    if (btnTriggerUpdate) btnTriggerUpdate.addEventListener('click', triggerDashboardUpdate);

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
    
    // Role-based view protection
    if (state.currentUser) {
        const isAdmin = state.currentUser.role === 'admin';
        const hasBackup = state.currentUser.permissions && state.currentUser.permissions.includes('backup');
        if (viewName === 'users' || viewName === 'installer') {
            if (!isAdmin) {
                window.location.hash = '#dashboard';
                return;
            }
        }
        if (viewName === 'backups') {
            if (!isAdmin && !hasBackup) {
                window.location.hash = '#dashboard';
                return;
            }
        }
    }
    
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
    } else if (viewName === 'users') {
        loadUsersList();
    } else if (viewName === 'settings') {
        loadSettingsInfo();
    } else if (viewName === 'installer') {
        loadGamesList();
    } else if (viewName === 'backups') {
        populateBackupsDropdown();
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

async function hideLogin() {
    el.loginContainer.classList.add('hidden');
    el.appContainer.classList.remove('hidden');
    
    try {
        const res = await fetch('/api/system/info');
        if (res.status === 200) {
            const data = await res.json();
            state.currentUser = data.user;
            state.lastSettingsData = data;
            renderSettingsMode(data);
            
            // Dynamically show the logged-in username in the sidebar footer
            const sidebarUsername = document.getElementById('sidebar-username');
            if (sidebarUsername && state.currentUser) {
                sidebarUsername.textContent = state.currentUser.username;
            }
            
            const menuUsersTab = document.getElementById('menu-users-tab');
            if (menuUsersTab) {
                if (state.currentUser && state.currentUser.role === 'admin') {
                    menuUsersTab.classList.remove('hidden');
                } else {
                    menuUsersTab.classList.add('hidden');
                    if (window.location.hash === '#users') {
                        window.location.hash = '#dashboard';
                    }
                }
            }

            const menuInstallerTab = document.getElementById('menu-installer-tab');
            if (menuInstallerTab) {
                if (state.currentUser && state.currentUser.role === 'admin') {
                    menuInstallerTab.classList.remove('hidden');
                } else {
                    menuInstallerTab.classList.add('hidden');
                    if (window.location.hash === '#installer') {
                        window.location.hash = '#dashboard';
                    }
                }
            }

            const menuBackupsTab = document.getElementById('menu-backups-tab');
            if (menuBackupsTab) {
                if (state.currentUser && (state.currentUser.role === 'admin' || (state.currentUser.permissions && state.currentUser.permissions.includes('backup')))) {
                    menuBackupsTab.classList.remove('hidden');
                } else {
                    menuBackupsTab.classList.add('hidden');
                    if (window.location.hash === '#backups') {
                        window.location.hash = '#dashboard';
                    }
                }
            }
        }
    } catch (e) {
        console.error('Failed to load user info:', e);
    }
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

function hasUserPermission(permission) {
    if (!state.currentUser) return false;
    if (state.currentUser.role === 'admin') return true;
    return state.currentUser.permissions && state.currentUser.permissions.includes(permission);
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
    
    const canStart = hasUserPermission('start');
    const canStop = hasUserPermission('stop');
    const canRestart = hasUserPermission('restart');
    const canConsole = hasUserPermission('console');
    const canConfig = hasUserPermission('config');
    const isAdmin = state.currentUser && state.currentUser.role === 'admin';

    // Build buttons HTML based on permissions
    let actionsHtml = '';
    if (canStart || canStop || canRestart) {
        actionsHtml += `<div class="card-actions">`;
        if (server.status === 'running') {
            if (canStop) {
                actionsHtml += `
                    <button class="btn btn-danger btn-server-action" onclick="runServerAction('${server.id}', 'stop')" ${isBusy ? 'disabled' : ''}>
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect></svg> ${t('btn-stop')}
                    </button>`;
            }
        } else {
            if (canStart) {
                actionsHtml += `
                    <button class="btn btn-success btn-server-action" onclick="runServerAction('${server.id}', 'start')" ${isBusy ? 'disabled' : ''}>
                        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg> ${t('btn-start')}
                    </button>`;
            }
        }
        if (canRestart) {
            actionsHtml += `
                <button class="btn btn-warning btn-server-action" onclick="runServerAction('${server.id}', 'restart')" ${isBusy ? 'disabled' : ''}>
                    <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67"></path></svg> ${t('btn-restart')}
                </button>`;
        }
        actionsHtml += `</div>`;
    }

    let subActionsHtml = '';
    if (canConsole || canConfig || isAdmin) {
        subActionsHtml += `<div class="card-actions-row-3">`;
        if (canConsole) {
            subActionsHtml += `
                <button class="btn btn-secondary btn-sm btn-server-action" onclick="openConsole('${server.id}')" ${isBusy ? 'disabled' : ''}>
                    ${t('btn-console')}
                </button>`;
        }
        if (canConfig) {
            subActionsHtml += `
                <button class="btn btn-secondary btn-sm btn-server-action" onclick="openConfigEditor('${server.id}')" ${isBusy ? 'disabled' : ''}>
                    ${t('btn-configs')}
                </button>`;
        }
        if (isAdmin) {
            subActionsHtml += `
                <button class="btn btn-secondary btn-sm btn-server-action" onclick="runServerAction('${server.id}', 'update')" ${isBusy ? 'disabled' : ''}>
                    ${t('btn-update')}
                </button>`;
        }
        subActionsHtml += `</div>`;
    }

    let adminActionsHtml = '';
    const canBackup = hasUserPermission('backup');
    if (isAdmin || canBackup) {
        adminActionsHtml += `
            <div class="card-actions-row-${(isAdmin ? 3 : 2)}" style="margin-top: 0.4rem;">
                <button class="btn btn-secondary btn-sm btn-server-action" onclick="runServerAction('${server.id}', 'details')" ${isBusy ? 'disabled' : ''}>
                    ${t('btn-details')}
                </button>
                <button class="btn btn-secondary btn-sm btn-server-action" onclick="runServerAction('${server.id}', 'backup')" ${isBusy ? 'disabled' : ''}>
                    ${t('btn-backup')}
                </button>`;
        if (isAdmin) {
            adminActionsHtml += `
                <button class="btn btn-secondary btn-sm btn-server-action" onclick="runServerAction('${server.id}', 'validate')" ${isBusy ? 'disabled' : ''}>
                    ${t('btn-validate')}
                </button>`;
        }
        adminActionsHtml += `</div>`;
        
        if (isAdmin) {
            adminActionsHtml += `
            <div class="card-actions-row-2" style="margin-top: 0.4rem; display: grid; grid-template-columns: 1fr 1fr; gap: 0.4rem;">
                <button class="btn btn-primary btn-sm btn-server-action" onclick="checkServerPorts('${server.id}')" style="display: flex; align-items: center; justify-content: center; gap: 4px; padding: 0.35rem 0.25rem; font-size: 0.75rem;">
                    <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="display: inline-block;"><circle cx="12" cy="12" r="10"></circle><path d="M12 16v-4M12 8h.01"></path></svg> ${t('btn-portcheck')}
                </button>
                <button class="btn btn-danger btn-sm btn-server-action" onclick="openDeleteServerModal('${server.id}')" style="display: flex; align-items: center; justify-content: center; gap: 4px; padding: 0.35rem 0.25rem; font-size: 0.75rem;">
                    <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" style="display: inline-block;"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg> ${t('btn-delete-server')}
                </button>
            </div>`;
        }
    }
    
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
        
        ${actionsHtml}
        ${subActionsHtml}
        ${adminActionsHtml}
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
        el.consoleBtnUpdateLgsm.disabled = true;
        el.consoleBtnForceUpdate.disabled = true;
        el.consoleBtnTestAlert.disabled = true;
        el.consoleInputField.disabled = true;
        el.consoleInputSubmit.disabled = true;
        el.terminalTitleText.textContent = t('console-no-server-connected');
        renderConsoleGameActions(null);
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
    el.consoleBtnUpdateLgsm.disabled = isBusy;
    el.consoleBtnForceUpdate.disabled = isBusy;
    el.consoleBtnTestAlert.disabled = isBusy;
    el.consoleInputField.disabled = !isRunning || isBusy;
    el.consoleInputSubmit.disabled = !isRunning || isBusy;
    
    const canBackup = hasUserPermission('backup');
    const isAdmin = state.currentUser && state.currentUser.role === 'admin';
    
    if (isAdmin || canBackup) {
        el.consoleBtnDetails.classList.remove('hidden');
        el.consoleBtnBackup.classList.remove('hidden');
    } else {
        el.consoleBtnDetails.classList.add('hidden');
        el.consoleBtnBackup.classList.add('hidden');
    }
    
    if (isAdmin) {
        el.consoleBtnValidate.classList.remove('hidden');
        el.consoleBtnUpdateLgsm.classList.remove('hidden');
        el.consoleBtnForceUpdate.classList.remove('hidden');
        el.consoleBtnTestAlert.classList.remove('hidden');
    } else {
        el.consoleBtnValidate.classList.add('hidden');
        el.consoleBtnUpdateLgsm.classList.add('hidden');
        el.consoleBtnForceUpdate.classList.add('hidden');
        el.consoleBtnTestAlert.classList.add('hidden');
    }
    
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
    
    renderConsoleGameActions(server);
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
        state.currentUser = data.user;
        
        el.settingsOs.textContent = data.os;
        el.settingsPid.textContent = data.pid;
        
        const versionVal = document.querySelector('[data-i18n="settings-info-version"]');
        if (versionVal && versionVal.nextElementSibling) {
            versionVal.nextElementSibling.textContent = data.version;
        }

        renderSettingsMode(data);
        if (data.mock) {
            el.settingsMode.className = 'info-value highlight text-warning';
        } else {
            el.settingsMode.className = 'info-value highlight text-success';
        }
        
        if (state.currentUser && state.currentUser.role === 'admin') {
            checkDashboardUpdate(false);
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
        resetSettingsAlerts();
    }
}

function handleSettingsServerChange() {
    const serverID = el.settingsServerSelect.value;
    if (!serverID) {
        el.settingsToolsArea.classList.add('hidden');
        resetSettingsAlerts();
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
    const isDe = state.language === 'de';
    const cronCode = `# LinuxGSM ${server.name} ${isDe ? 'Geplante Wartungsaufgaben' : 'Scheduled Maintenance Tasks'}
# 1. ${isDe ? 'Server auf Abstürze überwachen (alle 5 Minuten)' : 'Monitor server for crashes (every 5 minutes)'}
*/5 * * * * /home/${server.user}/${server.script} monitor > /dev/null 2>&1

# 2. ${isDe ? 'Automatische Updateprüfung (alle 30 Minuten, startet bei Update neu)' : 'Automatic update check (every 30 minutes, restarts on update)'}
*/30 * * * * /home/${server.user}/${server.script} update > /dev/null 2>&1

# 3. ${isDe ? 'Täglicher Updatecheck & Neustart um 04:30 Uhr nachts' : 'Daily update check & restart at 04:30 AM'}
30 4 * * * /home/${server.user}/${server.script} force-update > /dev/null 2>&1

# 4. ${isDe ? 'Wöchentliches Update von LinuxGSM selbst (jeden Sonntag um 00:00 Uhr)' : 'Weekly update of LinuxGSM itself (every Sunday at 00:00 AM)'}
0 0 * * 0 /home/${server.user}/${server.script} update-lgsm > /dev/null 2>&1`;

    el.templateSystemd.textContent = systemdCode;
    el.templateCron.textContent = cronCode;
    el.settingsToolsArea.classList.remove('hidden');
    
    // 3. Configure alerts and load settings
    const isAdmin = state.currentUser && state.currentUser.role === 'admin';
    if (isAdmin) {
        el.alertsSettingDiscordEnabled.disabled = false;
        el.alertsSettingDiscordWebhook.disabled = false;
        el.alertsSettingTelegramEnabled.disabled = false;
        el.alertsSettingTelegramToken.disabled = false;
        el.alertsSettingTelegramChatid.disabled = false;
        el.alertsSettingEmailEnabled.disabled = false;
        el.alertsSettingEmailSmtp.disabled = false;
        el.alertsSettingEmailPort.disabled = false;
        el.alertsSettingEmailUser.disabled = false;
        el.alertsSettingEmailPass.disabled = false;
        el.alertsSettingEmailDest.disabled = false;
        el.alertsSettingsSaveBtn.disabled = false;
        loadAlertSettings();
    } else {
        resetSettingsAlerts();
    }
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
        "btn-force-update": "Force Update",
        "btn-test-alert": "Test Alert",
        "alerts-settings-title": "Notification Alerts Settings",
        "alerts-discord-webhook-label": "Discord Webhook URL",
        "settings-tools-btn-systemd": "Install & Enable Service",
        "settings-tools-btn-cron": "Write Cronjobs to Crontab",
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
        "settings-tools-systemd-title": "Systemd Service (Autostart on Boot)",
        "settings-tools-systemd-desc": "Create a file under /etc/systemd/system/lgsm-[server].service as root and add the following content:",
        "settings-tools-cron-title": "Cronjobs (Scheduled Maintenance Tasks)",
        "settings-tools-cron-desc": "Add these lines to the server user's crontab (or via crontab -e as root):",
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
        "modal-delete-submit-btn": "Delete Server Permanently",
        "console-no-server-connected": "No server connected",
        "menu-users": "Users",
        "users-title": "User Administration",
        "users-subtitle": "Create and manage dashboard users and their access permissions.",
        "btn-new-user": "New User",
        "col-username": "Username",
        "col-role": "Role",
        "col-servers": "Assigned Servers",
        "col-permissions": "Permissions",
        "col-actions": "Actions",
        "role-user": "Normal User",
        "role-admin": "Administrator",
        "user-servers-label": "Server Assignment",
        "user-servers-help": "Administrators automatically have access to all servers.",
        "user-permissions-label": "Allowed Functions",
        "user-permissions-help": "Administrators automatically have all permission scopes.",
        "perm-start": "Start server",
        "perm-stop": "Stop server",
        "perm-restart": "Restart server",
        "perm-console": "Live console",
        "perm-config": "Edit configs",
        "settings-update-title": "Dashboard Updates",
        "settings-update-desc": "Check for new versions of the dashboard on GitHub.",
        "settings-update-status-label": "Status:",
        "update-status-latest": "Up to date",
        "settings-update-new-version": "Available Version:",
        "btn-check-update": "Check for Updates",
        "btn-trigger-update": "Update Now",
        "modal-user-title-new": "Create New User",
        "modal-user-subtitle": "Assign user roles and permissions.",
        "user-username-help": "3-16 characters, lowercase letters, numbers and underscores only.",
        "btn-cancel": "Cancel",
        "menu-backups": "Backups",
        "backups-title": "Backups",
        "backups-subtitle": "List, create, download, delete and restore server backups.",
        "backups-select-default": "-- Select a server --",
        "backups-btn-create": "Create Backup",
        "backups-btn-upload": "Upload Backup",
        "backups-upload-invalid-ext": "Only .tar.gz files are allowed.",
        "backups-upload-success": "Backup uploaded successfully.",
        "backups-upload-error": "Failed to upload backup due to a network error.",
        "backups-no-server-selected": "No server selected",
        "backups-empty": "No backups found for this server.",
        "col-filename": "Filename",
        "col-date": "Date",
        "col-size": "Size",
        "btn-download": "Download",
        "btn-restore": "Restore",
        "btn-delete": "Delete",
        "perm-backup": "Manage backups",
        "confirm-delete-backup-title": "Delete Backup",
        "confirm-delete-backup-text": "Are you sure you want to delete this backup? This action cannot be undone.",
        "confirm-restore-backup-title": "Restore Backup",
        "confirm-restore-backup-text": "Are you sure you want to restore this backup? The game server will be stopped and all files will be replaced with the state of this backup.",
        "backups-settings-title": "Backup Settings",
        "backups-setting-maxbackups-label": "Max Backups (maxbackups)",
        "backups-setting-maxbackups-help": "Set the maximum number of backups to retain. 0 prevents backups from being saved.",
        "backups-setting-maxbackupdays-label": "Max Backup Days (maxbackupdays)",
        "backups-setting-maxbackupdays-help": "Backups older than this number of days will be deleted. 0 retains backup for 24 hours.",
        "backups-setting-stoponbackup-label": "Stop on Backup (stoponbackup)",
        "backups-setting-stoponbackup-help": "Stop the server while compressing to prevent file changes and corruption.",
        "option-on": "On",
        "option-off": "Off",
        "backups-settings-saved": "Backup settings saved successfully!",
        "backups-settings-save-error": "Failed to save backup settings.",
        "backups-setting-autobackup-label": "Enable automatic backups (Cronjob)",
        "backups-setting-cron-preset-label": "Backup Interval",
        "cron-preset-daily": "Daily at 05:00 AM (Recommended)",
        "cron-preset-weekly": "Weekly on Sunday at 04:00 AM",
        "cron-preset-12h": "Every 12 Hours",
        "cron-preset-custom": "Custom (Cron Expression)",
        "backups-setting-cron-custom-label": "Cron Expression",
        "backups-setting-cron-custom-help": "Format: Minute Hour Day-of-Month Month Day-of-Week"
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
        "btn-force-update": "Force Update",
        "btn-test-alert": "Alert Testen",
        "alerts-settings-title": "Benachrichtigungs-Einstellungen",
        "alerts-discord-webhook-label": "Discord Webhook URL",
        "settings-tools-btn-systemd": "Dienst automatisch installieren & aktivieren",
        "settings-tools-btn-cron": "Cronjobs in Crontab schreiben",
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
        "modal-delete-submit-btn": "Server unwiderruflich löschen",
        "console-no-server-connected": "Kein Server verbunden",
        "menu-users": "Benutzer",
        "users-title": "Benutzerverwaltung",
        "users-subtitle": "Erstelle und verwalte Dashboard-Benutzer und deren Zugriffsrechte.",
        "btn-new-user": "Neuer Benutzer",
        "col-username": "Benutzername",
        "col-role": "Rolle",
        "col-servers": "Zugeordnete Server",
        "col-permissions": "Rechte",
        "col-actions": "Aktionen",
        "role-user": "Normaler Benutzer",
        "role-admin": "Administrator",
        "user-servers-label": "Server-Zuweisung",
        "user-servers-help": "Administratoren haben automatisch Zugriff auf alle Server.",
        "user-permissions-label": "Erlaubter Funktionsumfang",
        "user-permissions-help": "Administratoren haben automatisch alle Berechtigungen.",
        "perm-start": "Server starten",
        "perm-stop": "Server stoppen",
        "perm-restart": "Server neustarten",
        "perm-console": "Live-Konsole",
        "perm-config": "Configs bearbeiten",
        "settings-update-title": "Dashboard-Updates",
        "settings-update-desc": "Überprüfe, ob neue Versionen des Dashboards auf GitHub verfügbar sind.",
        "settings-update-status-label": "Status:",
        "update-status-latest": "Aktuell",
        "settings-update-new-version": "Verfügbare Version:",
        "btn-check-update": "Nach Updates suchen",
        "btn-trigger-update": "Jetzt updaten",
        "modal-user-title-new": "Neuen Benutzer anlegen",
        "modal-user-subtitle": "Benutzerrollen und Berechtigungen zuweisen.",
        "user-username-help": "3-16 Zeichen, nur Kleinbuchstaben, Zahlen und Unterstriche.",
        "btn-cancel": "Abbrechen",
        "menu-backups": "Backups",
        "backups-title": "Backups",
        "backups-subtitle": "Backups auflisten, erstellen, herunterladen, löschen und wiederherstellen.",
        "backups-select-default": "-- Server auswählen --",
        "backups-btn-create": "Backup erstellen",
        "backups-btn-upload": "Backup hochladen",
        "backups-upload-invalid-ext": "Nur .tar.gz-Dateien sind erlaubt.",
        "backups-upload-success": "Backup erfolgreich hochgeladen.",
        "backups-upload-error": "Fehler beim Hochladen des Backups aufgrund eines Netzwerkfehlers.",
        "backups-no-server-selected": "Kein Server ausgewählt",
        "backups-empty": "Keine Backups für diesen Server gefunden.",
        "col-filename": "Dateiname",
        "col-date": "Datum",
        "col-size": "Größe",
        "btn-download": "Herunterladen",
        "btn-restore": "Wiederherstellen",
        "btn-delete": "Löschen",
        "perm-backup": "Backups verwalten",
        "confirm-delete-backup-title": "Backup löschen",
        "confirm-delete-backup-text": "Möchtest du dieses Backup wirklich löschen? Dieser Schritt kann nicht rückgängig gemacht werden.",
        "confirm-restore-backup-title": "Backup wiederherstellen",
        "confirm-restore-backup-text": "Möchtest du dieses Backup wirklich wiederherstellen? Der Gameserver wird gestoppt und alle Spieldateien werden auf den Stand dieses Backups zurückgesetzt.",
        "backups-settings-title": "Backup-Einstellungen",
        "backups-setting-maxbackups-label": "Max Backups (maxbackups)",
        "backups-setting-maxbackups-help": "Maximale Anzahl an Backups, die behalten werden. 0 verhindert das Speichern von Backups.",
        "backups-setting-maxbackupdays-label": "Max Backup-Tage (maxbackupdays)",
        "backups-setting-maxbackupdays-help": "Backups, die älter als diese Anzahl an Tagen sind, werden gelöscht. 0 behält das Backup für 24 Stunden.",
        "backups-setting-stoponbackup-label": "Server stoppen bei Backup (stoponbackup)",
        "backups-setting-stoponbackup-help": "Stoppt den Server während der Komprimierung, um Dateiveränderungen und Datenkorruption zu verhindern.",
        "option-on": "An",
        "option-off": "Aus",
        "backups-settings-saved": "Backup-Einstellungen erfolgreich gespeichert!",
        "backups-settings-save-error": "Fehler beim Speichern der Backup-Einstellungen.",
        "backups-setting-autobackup-label": "Automatische Backups aktivieren (Cronjob)",
        "backups-setting-cron-preset-label": "Backup-Intervall",
        "cron-preset-daily": "Täglich um 05:00 Uhr (Empfohlen)",
        "cron-preset-weekly": "Wöchentlich am Sonntag um 04:00 Uhr",
        "cron-preset-12h": "Alle 12 Stunden",
        "cron-preset-custom": "Benutzerdefiniert (Cron-Ausdruck)",
        "backups-setting-cron-custom-label": "Cron-Ausdruck",
        "backups-setting-cron-custom-help": "Format: Minute Stunde Tag-des-Monats Monat Tag-der-Woche"
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
    
    const sunIconMobile = document.querySelector('.sun-icon-mobile');
    const moonIconMobile = document.querySelector('.moon-icon-mobile');
    
    if (theme === 'light') {
        document.body.classList.add('light-theme');
        el.themeIconSun.classList.add('hidden');
        el.themeIconMoon.classList.remove('hidden');
        if (sunIconMobile) sunIconMobile.classList.add('hidden');
        if (moonIconMobile) moonIconMobile.classList.remove('hidden');
    } else {
        document.body.classList.remove('light-theme');
        el.themeIconSun.classList.remove('hidden');
        el.themeIconMoon.classList.add('hidden');
        if (sunIconMobile) sunIconMobile.classList.remove('hidden');
        if (moonIconMobile) moonIconMobile.classList.add('hidden');
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

// -------------------------------------------------------------
// Dashboard Update Logic
// -------------------------------------------------------------
let latestTagName = '';

async function checkDashboardUpdate(manual = false) {
    const statusText = document.getElementById('update-status-text');
    const detailsRow = document.getElementById('update-details-row');
    const newVersionText = document.getElementById('update-new-version-text');
    const btnTrigger = document.getElementById('btn-trigger-update');
    const messageArea = document.getElementById('settings-update-message');
    
    if (!statusText) return;
    
    if (manual) {
        messageArea.textContent = state.language === 'de' ? 'Suche nach Updates...' : 'Checking for updates...';
        messageArea.className = 'info-message text-muted';
    }
    
    try {
        const res = await apiFetch('/api/admin/update/check');
        const data = await res.json();
        
        if (data.has_update) {
            statusText.textContent = state.language === 'de' ? 'Update verfügbar' : 'Update Available';
            statusText.className = 'info-value text-warning';
            newVersionText.textContent = data.latest_version;
            detailsRow.classList.remove('hidden');
            btnTrigger.classList.remove('hidden');
            latestTagName = data.latest_version;
            
            if (manual) {
                messageArea.textContent = state.language === 'de' ? 'Eine neue Version ist verfügbar!' : 'A new version is available!';
                messageArea.className = 'info-message text-warning';
            }
        } else {
            statusText.textContent = state.language === 'de' ? 'Aktuell' : 'Up to date';
            statusText.className = 'info-value text-success';
            detailsRow.classList.add('hidden');
            btnTrigger.classList.add('hidden');
            
            if (manual) {
                messageArea.textContent = state.language === 'de' ? 'Dashboard ist auf dem neuesten Stand.' : 'Dashboard is up to date.';
                messageArea.className = 'info-message text-success';
            }
        }
    } catch (e) {
        console.error('Update check failed:', e);
        if (manual) {
            messageArea.textContent = state.language === 'de' ? 'Fehler beim Überprüfen von Updates.' : 'Error checking for updates.';
            messageArea.className = 'info-message text-danger';
        }
    }
}

async function triggerDashboardUpdate() {
    const messageArea = document.getElementById('settings-update-message');
    const btnTrigger = document.getElementById('btn-trigger-update');
    const btnCheck = document.getElementById('btn-check-update');
    
    if (!latestTagName) {
        alert('Kein Update-Tag vorhanden.');
        return;
    }
    
    if (!confirm(state.language === 'de' ? 'Möchtest du das Dashboard wirklich aktualisieren? Die Verbindung wird kurz getrennt.' : 'Are you sure you want to update the dashboard? The connection will be briefly disconnected.')) {
        return;
    }
    
    messageArea.textContent = state.language === 'de' ? 'Update wird lokal kompiliert... Bitte warten.' : 'Update compiling locally... Please wait.';
    messageArea.className = 'info-message text-warning';
    btnTrigger.disabled = true;
    btnCheck.disabled = true;
    
    try {
        const res = await apiFetch('/api/admin/update/trigger', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ tag_name: latestTagName })
        });
        
        if (res.status === 200) {
            messageArea.textContent = state.language === 'de' ? 'Update erfolgreich! Dashboard startet neu...' : 'Update successful! Dashboard is restarting...';
            messageArea.className = 'info-message text-success';
            
            setTimeout(() => {
                window.location.reload();
            }, 4000);
        } else {
            const data = await res.json();
            throw new Error(data.error || 'Server error');
        }
    } catch (e) {
        console.error('Update failed:', e);
        messageArea.textContent = (state.language === 'de' ? 'Update fehlgeschlagen: ' : 'Update failed: ') + e.message;
        messageArea.className = 'info-message text-danger';
        btnTrigger.disabled = false;
        btnCheck.disabled = false;
    }
}

// -------------------------------------------------------------
// User Management Logic
// -------------------------------------------------------------
let currentEditUser = null;

async function loadUsersList() {
    const tbody = document.getElementById('users-list-body');
    if (!tbody) return;
    
    tbody.innerHTML = `<tr><td colspan="5" style="text-align:center; padding: 2rem; color: var(--text-muted);">${state.language === 'de' ? 'Lade Benutzer...' : 'Loading users...'}</td></tr>`;
    
    try {
        const res = await apiFetch('/api/admin/users');
        const users = await res.json();
        
        tbody.innerHTML = '';
        if (users.length === 0) {
            tbody.innerHTML = `<tr><td colspan="5" style="text-align:center; padding: 2rem; color: var(--text-muted);">${state.language === 'de' ? 'Keine Benutzer gefunden.' : 'No users found.'}</td></tr>`;
            return;
        }
        
        users.forEach(user => {
            const tr = document.createElement('tr');
            tr.style.borderBottom = '1px solid var(--border-color)';
            
            const roleBadge = user.role === 'admin' ? 
                '<span class="badge badge-success" style="font-size: 0.75rem;">Admin</span>' : 
                '<span class="badge badge-secondary" style="font-size: 0.75rem;">User</span>';
                
            const servers = user.role === 'admin' ? 
                `<span class="text-muted" style="font-size: 0.8rem;">${state.language === 'de' ? 'Alle Server' : 'All Servers'}</span>` : 
                (user.servers && user.servers.length > 0 ? 
                    user.servers.map(s => `<code style="font-size: 0.8rem;">${s}</code>`).join(', ') : 
                    `<span class="text-muted" style="font-size: 0.8rem;">${state.language === 'de' ? 'Keine' : 'None'}</span>`);
                    
            const perms = user.role === 'admin' ? 
                `<span class="text-muted" style="font-size: 0.8rem;">${state.language === 'de' ? 'Voller Zugriff' : 'Full Access'}</span>` : 
                (user.permissions && user.permissions.length > 0 ? 
                    user.permissions.map(p => {
                        let label = p;
                        if (state.language === 'de') {
                            if (p === 'start') label = 'Start';
                            if (p === 'stop') label = 'Stop';
                            if (p === 'restart') label = 'Restart';
                            if (p === 'console') label = 'Konsole';
                            if (p === 'config') label = 'Configs';
                        }
                        return `<span class="badge badge-pulse" style="font-size: 0.7rem; background: rgba(255,255,255,0.05); color: var(--text-color); border: 1px solid var(--border-color);">${label}</span>`;
                    }).join(' ') : 
                    `<span class="text-muted" style="font-size: 0.8rem;">${state.language === 'de' ? 'Keine' : 'None'}</span>`);
            
            const isSelf = state.currentUser && state.currentUser.username === user.username;
            const deleteBtn = isSelf ? 
                `<button class="btn btn-danger btn-sm" disabled style="opacity: 0.4;">${state.language === 'de' ? 'Löschen' : 'Delete'}</button>` : 
                `<button class="btn btn-danger btn-sm" onclick="deleteUser('${user.username}')">${state.language === 'de' ? 'Löschen' : 'Delete'}</button>`;
                
            tr.innerHTML = `
                <td style="padding: 0.75rem;"><strong>${user.username}</strong> ${isSelf ? `<small class="text-muted">(${state.language === 'de' ? 'Du' : 'You'})</small>` : ''}</td>
                <td style="padding: 0.75rem;">${roleBadge}</td>
                <td style="padding: 0.75rem;">${servers}</td>
                <td style="padding: 0.75rem;">${perms}</td>
                <td style="padding: 0.75rem; text-align: right; display: flex; gap: 0.5rem; justify-content: flex-end;">
                    <button class="btn btn-secondary btn-sm" onclick="openEditUserModal('${user.username}')">${state.language === 'de' ? 'Bearbeiten' : 'Edit'}</button>
                    ${deleteBtn}
                </td>
            `;
            tbody.appendChild(tr);
        });
    } catch (e) {
        console.error('Failed to load users:', e);
        tbody.innerHTML = `<tr><td colspan="5" style="text-align:center; padding: 2rem; color: var(--text-danger);">${state.language === 'de' ? 'Fehler beim Laden der Benutzer.' : 'Failed to load users.'}</td></tr>`;
    }
}

async function populateUserServerCheckboxes() {
    const listDiv = document.getElementById('user-servers-list');
    if (!listDiv) return;
    
    listDiv.innerHTML = '';
    
    try {
        const res = await fetch('/api/servers');
        if (res.status === 200) {
            const servers = await res.json();
            if (servers.length === 0) {
                listDiv.innerHTML = `<span class="text-muted" style="grid-column: 1 / -1; font-size: 0.8rem; padding: 0.5rem;">${state.language === 'de' ? 'Keine Server installiert.' : 'No servers installed.'}</span>`;
                return;
            }
            
            servers.forEach(srv => {
                const label = document.createElement('label');
                label.className = 'checkbox-label';
                label.style.display = 'flex';
                label.style.alignItems = 'center';
                label.style.gap = '0.5rem';
                label.style.cursor = 'pointer';
                label.style.fontSize = '0.85rem';
                
                label.innerHTML = `
                    <input type="checkbox" name="user-assigned-servers" value="${srv.id}">
                    <span>${srv.name} (<code>${srv.user}</code>)</span>
                `;
                listDiv.appendChild(label);
            });
        }
    } catch (e) {
        console.error('Failed to load servers for user form:', e);
    }
}

async function openCreateUserModal() {
    currentEditUser = null;
    
    const title = document.getElementById('modal-user-title');
    const usernameInput = document.getElementById('user-username');
    const passwordInput = document.getElementById('user-password');
    const passwordLabel = document.getElementById('user-password-label');
    const passwordHelp = document.getElementById('user-password-help');
    const roleSelect = document.getElementById('user-role');
    const messageArea = document.getElementById('modal-user-message');
    const modal = document.getElementById('modal-user');
    
    title.textContent = state.language === 'de' ? 'Neuen Benutzer anlegen' : 'Create New User';
    usernameInput.disabled = false;
    usernameInput.value = '';
    passwordInput.value = '';
    passwordInput.required = true;
    passwordLabel.textContent = state.language === 'de' ? 'Passwort' : 'Password';
    passwordHelp.classList.add('hidden');
    roleSelect.value = 'user';
    messageArea.innerHTML = '';
    
    await populateUserServerCheckboxes();
    updateUserFormVisibility();
    
    modal.classList.remove('hidden');
}

async function openEditUserModal(username) {
    currentEditUser = username;
    
    const title = document.getElementById('modal-user-title');
    const usernameInput = document.getElementById('user-username');
    const passwordInput = document.getElementById('user-password');
    const passwordLabel = document.getElementById('user-password-label');
    const passwordHelp = document.getElementById('user-password-help');
    const roleSelect = document.getElementById('user-role');
    const messageArea = document.getElementById('modal-user-message');
    const modal = document.getElementById('modal-user');
    
    title.textContent = (state.language === 'de' ? 'Benutzer bearbeiten: ' : 'Edit User: ') + username;
    usernameInput.disabled = true;
    usernameInput.value = username;
    passwordInput.value = '';
    passwordInput.required = false;
    passwordLabel.textContent = (state.language === 'de' ? 'Neues Passwort' : 'New Password') + ` (${state.language === 'de' ? 'optional' : 'optional'})`;
    passwordHelp.classList.remove('hidden');
    messageArea.innerHTML = '';
    
    await populateUserServerCheckboxes();
    
    try {
        const res = await apiFetch('/api/admin/users');
        const users = await res.json();
        const user = users.find(u => u.username === username);
        if (user) {
            roleSelect.value = user.role;
            
            const serverCheckboxes = document.querySelectorAll('input[name="user-assigned-servers"]');
            serverCheckboxes.forEach(cb => {
                cb.checked = user.servers && user.servers.includes(cb.value);
            });
            
            const permCheckboxes = document.querySelectorAll('#user-permissions-list input[type="checkbox"]');
            permCheckboxes.forEach(cb => {
                cb.checked = user.permissions && user.permissions.includes(cb.value);
            });
        }
    } catch (e) {
        console.error('Failed to load user details for editing:', e);
    }
    
    updateUserFormVisibility();
    modal.classList.remove('hidden');
}

function updateUserFormVisibility() {
    const roleSelect = document.getElementById('user-role');
    const serversSection = document.getElementById('user-servers-section');
    const permissionsSection = document.getElementById('user-permissions-section');
    
    if (roleSelect.value === 'admin') {
        serversSection.classList.add('hidden');
        permissionsSection.classList.add('hidden');
    } else {
        serversSection.classList.remove('hidden');
        permissionsSection.classList.remove('hidden');
    }
}

async function saveUserForm() {
    const usernameInput = document.getElementById('user-username');
    const passwordInput = document.getElementById('user-password');
    const roleSelect = document.getElementById('user-role');
    const messageArea = document.getElementById('modal-user-message');
    const modal = document.getElementById('modal-user');
    
    const username = usernameInput.value.trim();
    const password = passwordInput.value;
    const role = roleSelect.value;
    
    if (!username) {
        messageArea.textContent = state.language === 'de' ? 'Benutzername ist erforderlich.' : 'Username is required.';
        messageArea.className = 'info-message text-danger';
        return;
    }
    
    if (!currentEditUser && !password) {
        messageArea.textContent = state.language === 'de' ? 'Passwort ist erforderlich.' : 'Password is required.';
        messageArea.className = 'info-message text-danger';
        return;
    }
    
    let servers = [];
    if (role === 'user') {
        const checkedServers = document.querySelectorAll('input[name="user-assigned-servers"]:checked');
        checkedServers.forEach(cb => servers.push(cb.value));
    }
    
    let permissions = [];
    if (role === 'user') {
        const checkedPerms = document.querySelectorAll('#user-permissions-list input[type="checkbox"]:checked');
        checkedPerms.forEach(cb => permissions.push(cb.value));
    } else {
        permissions = ['start', 'stop', 'restart', 'console', 'config', 'backup'];
    }
    
    messageArea.textContent = state.language === 'de' ? 'Wird gespeichert...' : 'Saving...';
    messageArea.className = 'info-message text-muted';
    
    try {
        let res;
        if (currentEditUser) {
            res = await apiFetch('/api/admin/users', {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    username: currentEditUser,
                    password: password || undefined,
                    role,
                    servers,
                    permissions
                })
            });
        } else {
            res = await apiFetch('/api/admin/users', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                    username,
                    password,
                    role,
                    servers,
                    permissions
                })
            });
        }
        
        const data = await res.json();
        if (res.status === 200 && data.status === 'ok') {
            modal.classList.add('hidden');
            loadUsersList();
        } else {
            throw new Error(data.error || 'Server error');
        }
    } catch (e) {
        console.error('Failed to save user:', e);
        messageArea.textContent = (state.language === 'de' ? 'Fehler beim Speichern: ' : 'Failed to save: ') + e.message;
        messageArea.className = 'info-message text-danger';
    }
}

async function deleteUser(username) {
    if (!confirm(state.language === 'de' ? `Möchtest du den Benutzer "${username}" wirklich unwiderruflich löschen?` : `Are you sure you want to permanently delete the user "${username}"?`)) {
        return;
    }
    
    try {
        const res = await apiFetch(`/api/admin/users?username=${username}`, {
            method: 'DELETE'
        });
        const data = await res.json();
        if (res.status === 200 && data.status === 'ok') {
            loadUsersList();
        } else {
            throw new Error(data.error || 'Server error');
        }
    } catch (e) {
        console.error('Failed to delete user:', e);
        alert((state.language === 'de' ? 'Fehler beim Löschen des Benutzers: ' : 'Failed to delete user: ') + e.message);
    }
}

window.deleteUser = deleteUser;
window.openEditUserModal = openEditUserModal;
window.confirmDeleteBackup = confirmDeleteBackup;
window.confirmRestoreBackup = confirmRestoreBackup;

// -------------------------------------------------------------
// Backups View Management
// -------------------------------------------------------------

function populateBackupsDropdown() {
    const current = el.backupsServerSelect.value;
    
    el.backupsServerSelect.innerHTML = `<option value="">${t('backups-select-default')}</option>`;
    
    state.servers.forEach(server => {
        const opt = document.createElement('option');
        opt.value = server.id;
        opt.textContent = `${server.name} (${server.user})`;
        el.backupsServerSelect.appendChild(opt);
    });
    
    if (current && state.servers.find(s => s.id === current)) {
        el.backupsServerSelect.value = current;
    } else {
        state.selectedBackupServer = '';
        updateBackupsView();
    }
}

function handleBackupsServerChange() {
    state.selectedBackupServer = el.backupsServerSelect.value;
    updateBackupsView();
}

function updateBackupsView() {
    if (!state.selectedBackupServer) {
        el.backupsBtnCreate.disabled = true;
        el.backupsBtnUpload.disabled = true;
        el.backupsTableBody.innerHTML = '';
        el.backupsEmptyMessage.classList.add('hidden');
        el.backupsLoading.classList.add('hidden');
        
        el.backupsSettingMaxBackups.disabled = true;
        el.backupsSettingMaxBackupDays.disabled = true;
        el.backupsSettingStopOnBackup.disabled = true;
        el.backupsSettingsSaveBtn.disabled = true;
        el.backupsSettingMaxBackups.value = '';
        el.backupsSettingMaxBackupDays.value = '';
        el.backupsSettingStopOnBackup.value = 'on';
        el.backupsSettingsMessage.textContent = '';
        
        el.backupsSettingAutoBackupEnabled.disabled = true;
        el.backupsSettingAutoBackupEnabled.checked = false;
        el.backupsSettingCronPreset.disabled = true;
        el.backupsSettingCronPreset.value = '0 5 * * *';
        el.backupsSettingCronCustom.disabled = true;
        el.backupsSettingCronCustom.value = '';
        el.backupsSettingCronContainer.classList.add('hidden');
        el.backupsSettingCustomCronContainer.classList.add('hidden');
        
        const placeholder = document.createElement('tr');
        placeholder.innerHTML = `<td colspan="4" class="text-center text-muted" style="padding: 2rem;">\${t('backups-no-server-selected')}</td>`;
        el.backupsTableBody.appendChild(placeholder);
        return;
    }
    
    el.backupsBtnCreate.disabled = false;
    el.backupsBtnUpload.disabled = false;
    
    el.backupsSettingMaxBackups.disabled = false;
    el.backupsSettingMaxBackupDays.disabled = false;
    el.backupsSettingStopOnBackup.disabled = false;
    el.backupsSettingsSaveBtn.disabled = false;
    el.backupsSettingsMessage.textContent = '';
    
    el.backupsSettingAutoBackupEnabled.disabled = false;
    el.backupsSettingCronPreset.disabled = false;
    el.backupsSettingCronCustom.disabled = false;
    
    loadBackupsList();
    loadBackupSettings();
}

async function loadBackupsList() {
    const serverId = state.selectedBackupServer;
    if (!serverId) return;
    
    el.backupsLoading.classList.remove('hidden');
    el.backupsEmptyMessage.classList.add('hidden');
    el.backupsTableBody.innerHTML = '';
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/backups`);
        el.backupsLoading.classList.add('hidden');
        
        if (res.status === 200) {
            const backups = await res.json();
            if (backups.length === 0) {
                el.backupsEmptyMessage.classList.remove('hidden');
                el.backupsEmptyMessage.textContent = t('backups-empty');
                return;
            }
            
            backups.forEach(backup => {
                const tr = document.createElement('tr');
                const sizeFormatted = formatBytes(backup.size);
                const dateObj = new Date(backup.date);
                const dateFormatted = dateObj.toLocaleString(state.language === 'de' ? 'de-DE' : 'en-US');
                const downloadUrl = `/api/servers/${serverId}/backups/download?file=${encodeURIComponent(backup.name)}`;
                
                tr.innerHTML = `
                    <td style="padding: 0.85rem 1rem;">
                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="margin-right: 0.5rem; vertical-align: middle; color: var(--text-secondary);"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline><line x1="16" y1="13" x2="8" y2="13"></line><line x1="16" y1="17" x2="8" y2="17"></line><polyline points="10 9 9 9 8 9"></polyline></svg>
                        <strong>${escapeHtml(backup.name)}</strong>
                    </td>
                    <td style="padding: 0.85rem 1rem;">${dateFormatted}</td>
                    <td style="padding: 0.85rem 1rem;">${sizeFormatted}</td>
                    <td style="padding: 0.85rem 1rem; display: flex; gap: 0.5rem;">
                        <a href="${downloadUrl}" class="btn btn-secondary btn-sm" download style="display: inline-flex; align-items: center; gap: 0.25rem;">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"></path></svg>
                            ${t('btn-download')}
                        </a>
                        <button class="btn btn-warning btn-sm" onclick="confirmRestoreBackup('${serverId}', '${escapeJs(backup.name)}')" style="display: inline-flex; align-items: center; gap: 0.25rem;">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 2v6h6M2.66 15.57a10 10 0 1 0-.57-8.38l5.67-5.67"></path></svg>
                            ${t('btn-restore')}
                        </button>
                        <button class="btn btn-danger btn-sm" onclick="confirmDeleteBackup('${serverId}', '${escapeJs(backup.name)}')" style="display: inline-flex; align-items: center; gap: 0.25rem;">
                            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                            ${t('btn-delete')}
                        </button>
                    </td>
                `;
                el.backupsTableBody.appendChild(tr);
            });
        }
    } catch (err) {
        el.backupsLoading.classList.add('hidden');
        console.error('Error loading backups:', err);
    }
}

async function loadBackupSettings() {
    const serverId = state.selectedBackupServer;
    if (!serverId) return;
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/backups/settings`);
        if (res.status === 200) {
            const settings = await res.json();
            el.backupsSettingMaxBackups.value = settings.maxbackups || '';
            el.backupsSettingMaxBackupDays.value = settings.maxbackupdays || '';
            el.backupsSettingStopOnBackup.value = settings.stoponbackup === 'off' ? 'off' : 'on';
            
            el.backupsSettingAutoBackupEnabled.checked = !!settings.autobackup_enabled;
            if (settings.autobackup_enabled) {
                el.backupsSettingCronContainer.classList.remove('hidden');
                const cron = settings.autobackup_cron || '';
                const presets = ['0 5 * * *', '0 4 * * 0', '0 */12 * * *'];
                if (presets.includes(cron)) {
                    el.backupsSettingCronPreset.value = cron;
                    el.backupsSettingCustomCronContainer.classList.add('hidden');
                    el.backupsSettingCronCustom.value = '';
                } else {
                    el.backupsSettingCronPreset.value = 'custom';
                    el.backupsSettingCustomCronContainer.classList.remove('hidden');
                    el.backupsSettingCronCustom.value = cron;
                }
            } else {
                el.backupsSettingCronContainer.classList.add('hidden');
                el.backupsSettingCustomCronContainer.classList.add('hidden');
                el.backupsSettingCronPreset.value = '0 5 * * *';
                el.backupsSettingCronCustom.value = '';
            }
        }
    } catch (err) {
        console.error('Error loading backup settings:', err);
    }
}

async function saveBackupSettings(e) {
    if (e) e.preventDefault();
    
    const serverId = state.selectedBackupServer;
    if (!serverId) return;
    
    el.backupsSettingsMessage.textContent = state.language === 'de' ? 'Wird gespeichert...' : 'Saving...';
    el.backupsSettingsMessage.className = 'info-message text-muted';
    el.backupsSettingsSaveBtn.disabled = true;
    
    try {
        let cronVal = '';
        if (el.backupsSettingAutoBackupEnabled.checked) {
            const preset = el.backupsSettingCronPreset.value;
            if (preset === 'custom') {
                cronVal = el.backupsSettingCronCustom.value.trim();
                if (!cronVal) {
                    throw new Error(state.language === 'de' ? 'Ein gültiger Cron-Ausdruck wird benötigt!' : 'A valid Cron expression is required!');
                }
            } else {
                cronVal = preset;
            }
        }
        
        const res = await apiFetch(`/api/servers/${serverId}/backups/settings`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                maxbackups: el.backupsSettingMaxBackups.value,
                maxbackupdays: el.backupsSettingMaxBackupDays.value,
                stoponbackup: el.backupsSettingStopOnBackup.value,
                autobackup_enabled: el.backupsSettingAutoBackupEnabled.checked,
                autobackup_cron: cronVal
            })
        });
        
        el.backupsSettingsSaveBtn.disabled = false;
        if (res.status === 200) {
            el.backupsSettingsMessage.textContent = t('backups-settings-saved');
            el.backupsSettingsMessage.className = 'info-message text-success';
            setTimeout(() => {
                if (state.selectedBackupServer === serverId) {
                    el.backupsSettingsMessage.textContent = '';
                }
            }, 3000);
        } else {
            const data = await res.json();
            el.backupsSettingsMessage.textContent = (state.language === 'de' ? 'Fehler: ' : 'Error: ') + (data.error || t('backups-settings-save-error'));
            el.backupsSettingsMessage.className = 'info-message text-danger';
        }
    } catch (err) {
        el.backupsSettingsSaveBtn.disabled = false;
        console.error('Error saving backup settings:', err);
        el.backupsSettingsMessage.textContent = (state.language === 'de' ? 'Fehler: ' : 'Error: ') + err.message;
        el.backupsSettingsMessage.className = 'info-message text-danger';
    }
}

async function handleBackupsUpload(e) {
    const file = e.target.files[0];
    if (!file) return;

    el.backupsInputUpload.value = '';

    const serverId = state.selectedBackupServer;
    if (!serverId) return;

    if (!file.name.endsWith('.tar.gz')) {
        alert(t('backups-upload-invalid-ext'));
        return;
    }

    el.backupsUploadProgressContainer.classList.remove('hidden');
    el.backupsUploadFilename.textContent = file.name;
    el.backupsUploadPercentage.textContent = '0%';
    el.backupsUploadProgressBar.style.width = '0%';

    el.backupsBtnUpload.disabled = true;
    el.backupsBtnCreate.disabled = true;

    const formData = new FormData();
    formData.append('backup', file);

    const xhr = new XMLHttpRequest();

    xhr.upload.addEventListener('progress', (event) => {
        if (event.lengthComputable) {
            const percent = Math.round((event.loaded / event.total) * 100);
            el.backupsUploadPercentage.textContent = `${percent}%`;
            el.backupsUploadProgressBar.style.width = `${percent}%`;
        }
    });

    xhr.addEventListener('load', () => {
        el.backupsUploadProgressContainer.classList.add('hidden');
        el.backupsBtnUpload.disabled = false;
        el.backupsBtnCreate.disabled = false;

        if (xhr.status >= 200 && xhr.status < 300) {
            alert(t('backups-upload-success'));
            loadBackupsList();
        } else {
            let errorMsg = 'Upload failed';
            try {
                const res = JSON.parse(xhr.responseText);
                errorMsg = res.error || errorMsg;
            } catch (err) {}
            alert((state.language === 'de' ? 'Fehler beim Hochladen: ' : 'Upload failed: ') + errorMsg);
        }
    });

    xhr.addEventListener('error', () => {
        el.backupsUploadProgressContainer.classList.add('hidden');
        el.backupsBtnUpload.disabled = false;
        el.backupsBtnCreate.disabled = false;
        alert(t('backups-upload-error'));
    });

    xhr.open('POST', `/api/servers/${serverId}/backups/upload`);
    xhr.send(formData);
}

function handleBackupsBtnCreateClick() {
    const serverId = state.selectedBackupServer;
    if (!serverId) return;
    
    runServerAction(serverId, 'backup');
}

function confirmDeleteBackup(serverId, filename) {
    if (confirm(`${t('confirm-delete-backup-text')} (${filename})`)) {
        deleteBackup(serverId, filename);
    }
}

async function deleteBackup(serverId, filename) {
    try {
        const res = await apiFetch(`/api/servers/${serverId}/backups/delete?file=${encodeURIComponent(filename)}`, {
            method: 'POST'
        });
        const data = await res.json();
        if (res.status === 200 && data.status === 'ok') {
            loadBackupsList();
        } else {
            alert((state.language === 'de' ? 'Fehler beim Löschen des Backups: ' : 'Error deleting backup: ') + (data.error || 'Unknown error'));
        }
    } catch (e) {
        console.error('Delete backup error:', e);
        alert((state.language === 'de' ? 'Fehler beim Löschen des Backups: ' : 'Error deleting backup: ') + e.message);
    }
}

function confirmRestoreBackup(serverId, filename) {
    if (confirm(`${t('confirm-restore-backup-text')} (${filename})`)) {
        runBackupRestoreAction(serverId, filename);
    }
}

function runBackupRestoreAction(serverId, filename) {
    const tAction = state.language === 'de' ? 'Wiederherstellung' : 'Restore';
    el.modalStreamTitle.textContent = (state.language === 'de' ? `Backup-Wiederherstellung für {server}` : `Backup Restore for {server}`).replace('{server}', serverId);
    el.modalStreamStatus.textContent = t('status-connecting');
    el.modalStreamStatus.className = 'badge badge-warning animate-pulse';
    el.modalStreamOutput.innerHTML = '';
    el.modalStreamClose.disabled = true;
    el.modalStream.classList.remove('hidden');
    
    const url = `/api/servers/${serverId}/backups/restore?file=${encodeURIComponent(filename)}&lang=${state.language}`;
    const eventSource = new EventSource(url);
    
    eventSource.onopen = () => {
        el.modalStreamStatus.textContent = state.language === 'de' ? 'Wiederherstellung läuft...' : 'Restoring backup...';
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
            
            loadBackupsList();
        }
    };
    
    eventSource.onerror = (err) => {
        const line = document.createElement('div');
        line.className = 'terminal-row text-danger';
        line.textContent = state.language === 'de' ? 'Fehler beim Verbinden mit der Wiederherstellung.' : 'Error connecting to the restore stream.';
        el.modalStreamOutput.appendChild(line);
        el.modalStreamStatus.textContent = t('status-cancelled');
        el.modalStreamStatus.className = 'badge badge-danger';
        el.modalStreamClose.disabled = false;
        eventSource.close();
    };
}

function formatBytes(bytes) {
    if (bytes === 0) return '0 Bytes';
    const k = 1024;
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

function escapeHtml(str) {
    return str.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;");
}

function escapeJs(str) {
    return str.replace(/'/g, "\\'").replace(/"/g, '\\"');
}

function toggleContainer(checkbox, container) {
    if (checkbox.checked) {
        container.classList.remove('hidden');
    } else {
        container.classList.add('hidden');
    }
}

async function loadAlertSettings() {
    const serverId = el.settingsServerSelect.value;
    if (!serverId) return;
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/alerts`);
        if (res.status === 200) {
            const data = await res.json();
            
            el.alertsSettingDiscordEnabled.checked = data.discord_enabled;
            el.alertsSettingDiscordWebhook.value = data.discord_webhook || '';
            toggleContainer(el.alertsSettingDiscordEnabled, el.alertsSettingDiscordContainer);
            
            el.alertsSettingTelegramEnabled.checked = data.telegram_enabled;
            el.alertsSettingTelegramToken.value = data.telegram_token || '';
            el.alertsSettingTelegramChatid.value = data.telegram_chatid || '';
            toggleContainer(el.alertsSettingTelegramEnabled, el.alertsSettingTelegramContainer);
            
            el.alertsSettingEmailEnabled.checked = data.email_enabled;
            el.alertsSettingEmailSmtp.value = data.email_smtp || '';
            el.alertsSettingEmailPort.value = data.email_port || '';
            el.alertsSettingEmailUser.value = data.email_user || '';
            el.alertsSettingEmailPass.value = data.email_pass || '';
            el.alertsSettingEmailDest.value = data.email_dest || '';
            toggleContainer(el.alertsSettingEmailEnabled, el.alertsSettingEmailContainer);
        }
    } catch (err) {
        console.error('Error loading alert settings:', err);
    }
}

async function saveAlertSettings(e) {
    if (e) e.preventDefault();
    
    const serverId = el.settingsServerSelect.value;
    if (!serverId) return;
    
    el.alertsSettingsMessage.textContent = state.language === 'de' ? 'Wird gespeichert...' : 'Saving...';
    el.alertsSettingsMessage.className = 'info-message text-muted';
    el.alertsSettingsSaveBtn.disabled = true;
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/alerts`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                discord_enabled: el.alertsSettingDiscordEnabled.checked,
                discord_webhook: el.alertsSettingDiscordWebhook.value.trim(),
                telegram_enabled: el.alertsSettingTelegramEnabled.checked,
                telegram_token: el.alertsSettingTelegramToken.value.trim(),
                telegram_chatid: el.alertsSettingTelegramChatid.value.trim(),
                email_enabled: el.alertsSettingEmailEnabled.checked,
                email_smtp: el.alertsSettingEmailSmtp.value.trim(),
                email_port: el.alertsSettingEmailPort.value.trim(),
                email_user: el.alertsSettingEmailUser.value.trim(),
                email_pass: el.alertsSettingEmailPass.value.trim(),
                email_dest: el.alertsSettingEmailDest.value.trim()
            })
        });
        
        el.alertsSettingsSaveBtn.disabled = false;
        if (res.status === 200) {
            el.alertsSettingsMessage.textContent = state.language === 'de' ? 'Benachrichtigungs-Einstellungen erfolgreich gespeichert.' : 'Alert settings saved successfully.';
            el.alertsSettingsMessage.className = 'info-message text-success';
            setTimeout(() => {
                if (el.settingsServerSelect.value === serverId) {
                    el.alertsSettingsMessage.textContent = '';
                }
            }, 3000);
        } else {
            const data = await res.json();
            el.alertsSettingsMessage.textContent = (state.language === 'de' ? 'Fehler: ' : 'Error: ') + (data.error || 'Unknown error');
            el.alertsSettingsMessage.className = 'info-message text-danger';
        }
    } catch (err) {
        el.alertsSettingsSaveBtn.disabled = false;
        console.error('Error saving alert settings:', err);
        el.alertsSettingsMessage.textContent = (state.language === 'de' ? 'Fehler: ' : 'Error: ') + err.message;
        el.alertsSettingsMessage.className = 'info-message text-danger';
    }
}

function resetSettingsAlerts() {
    el.alertsSettingDiscordEnabled.disabled = true;
    el.alertsSettingDiscordEnabled.checked = false;
    el.alertsSettingDiscordWebhook.disabled = true;
    el.alertsSettingDiscordWebhook.value = '';
    el.alertsSettingDiscordContainer.classList.add('hidden');
    
    el.alertsSettingTelegramEnabled.disabled = true;
    el.alertsSettingTelegramEnabled.checked = false;
    el.alertsSettingTelegramToken.disabled = true;
    el.alertsSettingTelegramToken.value = '';
    el.alertsSettingTelegramChatid.disabled = true;
    el.alertsSettingTelegramChatid.value = '';
    el.alertsSettingTelegramContainer.classList.add('hidden');
    
    el.alertsSettingEmailEnabled.disabled = true;
    el.alertsSettingEmailEnabled.checked = false;
    el.alertsSettingEmailSmtp.disabled = true;
    el.alertsSettingEmailSmtp.value = '';
    el.alertsSettingEmailPort.disabled = true;
    el.alertsSettingEmailPort.value = '';
    el.alertsSettingEmailUser.disabled = true;
    el.alertsSettingEmailUser.value = '';
    el.alertsSettingEmailPass.disabled = true;
    el.alertsSettingEmailPass.value = '';
    el.alertsSettingEmailDest.disabled = true;
    el.alertsSettingEmailDest.value = '';
    el.alertsSettingEmailContainer.classList.add('hidden');
    
    el.alertsSettingsSaveBtn.disabled = true;
    el.alertsSettingsMessage.textContent = '';
}

async function installSystemdService() {
    const serverId = el.settingsServerSelect.value;
    if (!serverId) return;
    
    if (!confirm(state.language === 'de' ? 'Bist du sicher, dass du den Systemd-Dienst für diesen Server installieren möchtest? Dies registriert den Server im System-Autostart.' : 'Are you sure you want to install the Systemd service for this server? This registers the server in the system autostart.')) {
        return;
    }
    
    el.settingsToolsBtnSystemd.disabled = true;
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/systemd/install`, {
            method: 'POST'
        });
        const data = await res.json();
        
        if (res.status === 200 && data.status === 'ok') {
            alert(state.language === 'de' ? 'Systemd-Dienst erfolgreich installiert und aktiviert!' : 'Systemd service successfully installed and enabled!');
        } else {
            alert((state.language === 'de' ? 'Fehler: ' : 'Error: ') + (data.error || 'Unknown error'));
        }
    } catch (err) {
        console.error('Systemd install error:', err);
        alert((state.language === 'de' ? 'Fehler: ' : 'Error: ') + err.message);
    } finally {
        el.settingsToolsBtnSystemd.disabled = false;
    }
}

async function installCronjobs() {
    const serverId = el.settingsServerSelect.value;
    if (!serverId) return;
    
    if (!confirm(state.language === 'de' ? 'Bist du sicher, dass du die LinuxGSM Wartungs-Cronjobs einrichten möchtest? Dies überschreibt bestehende Wartungs-Cronjobs dieses Servers.' : 'Are you sure you want to write the LinuxGSM maintenance cronjobs? This will replace existing maintenance cronjobs for this server.')) {
        return;
    }
    
    el.settingsToolsBtnCron.disabled = true;
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/cron/install`, {
            method: 'POST'
        });
        const data = await res.json();
        
        if (res.status === 200 && data.status === 'ok') {
            alert(state.language === 'de' ? 'Cronjobs erfolgreich in die Crontab geschrieben!' : 'Cronjobs successfully written to crontab!');
        } else {
            alert((state.language === 'de' ? 'Fehler: ' : 'Error: ') + (data.error || 'Unknown error'));
        }
    } catch (err) {
        console.error('Cron install error:', err);
        alert((state.language === 'de' ? 'Fehler: ' : 'Error: ') + err.message);
    } finally {
        el.settingsToolsBtnCron.disabled = false;
    }
}

function renderConsoleGameActions(server) {
    if (!server) {
        el.consoleGameActions.classList.add('hidden');
        el.consoleGameActions.innerHTML = '';
        return;
    }

    const isAdmin = state.currentUser && state.currentUser.role === 'admin';
    const isBusy = server.status === 'installing' || server.status === 'updating';

    if (!isAdmin) {
        el.consoleGameActions.classList.add('hidden');
        el.consoleGameActions.innerHTML = '';
        return;
    }

    if (server.game === 'rust') {
        el.consoleGameActions.innerHTML = `
            <button id="console-btn-map-wipe" class="btn btn-danger btn-sm" \${isBusy ? 'disabled' : ''}>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"></path></svg>
                <span>Map Wipe</span>
            </button>
            <button id="console-btn-full-wipe" class="btn btn-danger btn-sm" \${isBusy ? 'disabled' : ''}>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                <span>Full Wipe</span>
            </button>
        `;
        el.consoleGameActions.classList.remove('hidden');

        document.getElementById('console-btn-map-wipe').addEventListener('click', () => {
            if (confirm(state.language === 'de' ? 'Bist du sicher, dass du ein Map-Wipe durchführen möchtest? Alle Kartendaten gehen verloren!' : 'Are you sure you want to perform a Map Wipe? All map progress will be deleted!')) {
                runServerAction(server.id, 'map-wipe');
            }
        });
        document.getElementById('console-btn-full-wipe').addEventListener('click', () => {
            if (confirm(state.language === 'de' ? 'Bist du sicher, dass du ein Full-Wipe durchführen möchtest? Alle Karten- und Blueprint-Daten gehen verloren!' : 'Are you sure you want to perform a Full Wipe? All map and blueprint database progress will be deleted!')) {
                runServerAction(server.id, 'full-wipe');
            }
        });
    } else if (server.game === 'ts3') {
        el.consoleGameActions.innerHTML = `
            <button id="console-btn-ts3-pw" class="btn btn-warning btn-sm" \${isBusy ? 'disabled' : ''}>
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"></rect><path d="M7 11V7a5 5 0 0 1 10 0v4"></path></svg>
                <span>Query Passwort ändern</span>
            </button>
        `;
        el.consoleGameActions.classList.remove('hidden');
        document.getElementById('console-btn-ts3-pw').addEventListener('click', () => {
            if (confirm(state.language === 'de' ? 'Möchtest du das Query-Admin-Passwort für diesen Teamspeak 3 Server zurücksetzen? Der Server startet danach neu.' : 'Do you want to reset the Query Admin password for this Teamspeak 3 Server? The server will restart.')) {
                runServerAction(server.id, 'change-password');
            }
        });
    } else {
        el.consoleGameActions.classList.add('hidden');
        el.consoleGameActions.innerHTML = '';
    }
}
