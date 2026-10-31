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
    formBaseContent: '', // text the config form was built from
    configLoadSeq: 0, // increases with every list or file load of the config editor
    configFileEntries: [], // {path, name, li} of the listed config files
    gridColumns: {}, // column choice per card grid (see COLUMN_GRIDS)
    theme: 'dark', // dark or light
    language: 'en', // en or de
    games: [],
    lastActiveView: 'dashboard'
};

// -------------------------------------------------------------
// CSRF protection: every state-changing request carries the per-session token
// -------------------------------------------------------------

function readCookie(name) {
    const prefix = name + '=';
    for (const part of document.cookie.split(';')) {
        const c = part.trim();
        if (c.startsWith(prefix)) return decodeURIComponent(c.slice(prefix.length));
    }
    return '';
}

function getCsrfToken() {
    return readCookie('csrf_token') || state.csrfToken || '';
}

const nativeFetch = window.fetch.bind(window);
window.fetch = (input, init = {}) => {
    const method = ((init && init.method) || (input && input.method) || 'GET').toUpperCase();
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
        const headers = new Headers((init && init.headers) || {});
        const token = getCsrfToken();
        if (token) headers.set('X-CSRF-Token', token);
        init = Object.assign({}, init, { headers, credentials: 'same-origin' });
    }
    return nativeFetch(input, init);
};

// Streams the Server-Sent-Event formatted output of a POST request. EventSource only supports
// GET, but actions that change state must never be triggered by a GET request.
async function streamPost(url, body, onEvent) {
    const res = await apiFetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body || {})
    });
    if (!res.ok || !res.body) {
        let msg = `HTTP ${res.status}`;
        try {
            const text = await res.text();
            try {
                const data = JSON.parse(text);
                if (data.error) msg = data.error;
            } catch (e) {
                if (text) msg = text.trim();
            }
        } catch (e) {
            // keep HTTP status message
        }
        throw new Error(msg);
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let sep;
        while ((sep = buffer.indexOf('\n\n')) !== -1) {
            const frame = buffer.slice(0, sep);
            buffer = buffer.slice(sep + 2);
            const data = frame.split('\n').filter(l => l.startsWith('data:')).map(l => l.slice(5).trim()).join('\n');
            if (data) {
                try {
                    onEvent(JSON.parse(data));
                } catch (e) {
                    console.error('Invalid stream event:', e);
                }
            }
        }
    }
}

// Opens the live log modal and streams a POST action into it.
async function runStreamModal({ title, runningText, url, body, errorText, onExit }) {
    el.modalStreamTitle.textContent = title;
    el.modalStreamStatus.textContent = t('status-connecting');
    el.modalStreamStatus.className = 'badge badge-warning animate-pulse';
    el.modalStreamOutput.innerHTML = '';
    el.modalStreamClose.disabled = true;
    el.modalStream.classList.remove('hidden');

    const appendLine = (text, className = 'terminal-row') => {
        const line = document.createElement('div');
        line.className = className;
        line.textContent = text;
        el.modalStreamOutput.appendChild(line);
        if (el.modalStreamAutoscroll.checked) {
            el.modalStreamOutput.scrollTop = el.modalStreamOutput.scrollHeight;
        }
    };

    let exited = false;
    try {
        el.modalStreamStatus.textContent = runningText;
        el.modalStreamStatus.className = 'badge badge-warning badge-pulse animate-pulse';
        await streamPost(url, body, (data) => {
            if (data.type === 'log') {
                appendLine(data.text);
            } else if (data.type === 'exit') {
                exited = true;
                // LinuxGSM ends with 3 after warnings: the command itself succeeded.
                const outcome = data.code === 0 ? ['status-success', 'badge-success'] : data.code === 3 ? ['status-warning', 'badge-warning'] : ['status-error', 'badge-danger'];
                el.modalStreamStatus.textContent = t(outcome[0]);
                el.modalStreamStatus.className = 'badge ' + outcome[1];
                el.modalStreamClose.disabled = false;
                if (onExit) onExit(data.code);
            }
        });
        if (!exited) throw new Error('stream closed');
    } catch (e) {
        appendLine(errorText + (e && e.message ? ` (${e.message})` : ''), 'terminal-row text-danger');
        el.modalStreamStatus.textContent = t('status-cancelled');
        el.modalStreamStatus.className = 'badge badge-danger';
        el.modalStreamClose.disabled = false;
    }
}

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
    consoleModeScript: document.getElementById('console-mode-script'),
    consoleModeGame: document.getElementById('console-mode-game'),
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
    settingsServerSelectBar: document.getElementById('settings-server-select-bar'),
    settingsServerSelect: document.getElementById('settings-server-select'),
    settingsServerTagInput: document.getElementById('settings-server-tag-input'),
    settingsServerTagSaveBtn: document.getElementById('settings-server-tag-save-btn'),
    settingsToolsArea: document.getElementById('settings-tools-area'),
    settingsCronArea: document.getElementById('settings-cron-area'),
    settingsServerPromptServer: document.getElementById('settings-server-prompt-server'),
    settingsServerPromptCron: document.getElementById('settings-server-prompt-cron'),
    settingsServerPromptAlerts: document.getElementById('settings-server-prompt-alerts'),
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
    consoleBtnCheckUpdate: document.getElementById('console-btn-check-update'),
    consoleBtnModsInstall: document.getElementById('console-btn-mods-install'),
    consoleBtnModsUpdate: document.getElementById('console-btn-mods-update'),
    consoleBtnFastdl: document.getElementById('console-btn-fastdl'),
    consoleBtnPostdetails: document.getElementById('console-btn-postdetails'),
    consoleBtnTestAlert: document.getElementById('console-btn-test-alert'),
    consoleGameActions: document.getElementById('console-game-actions'),
    
    // File Manager & Audit Log elements
    menuFilesTab: document.getElementById('menu-files-tab'),
    menuAuditTab: document.getElementById('menu-audit-tab'),
    sectionFiles: document.getElementById('view-files'),
    sectionAudit: document.getElementById('view-audit'),
    filesServerSelect: document.getElementById('files-server-select'),
    filesTableBody: document.getElementById('files-table-body'),
    filesEditorCard: document.getElementById('files-editor-card'),
    filesEditorTextarea: document.getElementById('files-editor-textarea'),
    filesEditingFilename: document.getElementById('files-editing-filename'),
    filesBtnSave: document.getElementById('files-btn-save'),
    filesBtnCloseEditor: document.getElementById('files-btn-close-editor'),
    filesBtnUpload: document.getElementById('files-btn-upload'),
    filesInputUpload: document.getElementById('files-input-upload'),
    filesBtnRefresh: document.getElementById('files-btn-refresh'),
    filesBreadcrumb: document.getElementById('files-breadcrumb'),
    auditTableBody: document.getElementById('audit-table-body'),
    auditBtnRefresh: document.getElementById('audit-btn-refresh'),
    modalMods: document.getElementById('modal-mods'),
    modalModsList: document.getElementById('modal-mods-list'),
    btnCloseModalMods: document.getElementById('btn-close-modal-mods'),
    btnCloseModsModalFooter: document.getElementById('btn-close-mods-modal-footer'),
    
    alertsFields: document.getElementById('alerts-fields'),
    alertsSettingsPanel: document.getElementById('alerts-settings-panel'),
    alertsSettingsSaveBtn: document.getElementById('alerts-settings-save-btn'),
    alertsSettingsMessage: document.getElementById('alerts-settings-message'),
    alertsSettingsForm: document.getElementById('alerts-settings-form'),
    
    settingsToolsBtnSystemd: document.getElementById('settings-tools-btn-systemd'),
    settingsToolsBtnCron: document.getElementById('settings-tools-btn-cron'),

    firewallStatusBadge: document.getElementById('firewall-status-badge'),
    firewallMessageText: document.getElementById('firewall-message-text'),
    firewallRulesBox: document.getElementById('firewall-rules-box'),

    modalPlayers: document.getElementById('modal-players'),
    modalPlayersClose: document.getElementById('modal-players-close'),
    modalPlayersTitle: document.getElementById('modal-players-title'),
    modalPlayersInfo: document.getElementById('modal-players-info'),
    modalPlayersTbody: document.getElementById('modal-players-tbody'),

    btnBulkStart: document.getElementById('btn-bulk-start'),
    btnBulkStop: document.getElementById('btn-bulk-stop'),
    btnBulkRestart: document.getElementById('btn-bulk-restart'),
    btnBulkUpdate: document.getElementById('btn-bulk-update'),
    filterTagSelect: document.getElementById('filter-tag-select'),

    cronJobs: document.getElementById('cron-jobs'),
    cronBuilderForm: document.getElementById('cron-builder-form'),
    cronBuilderMessage: document.getElementById('cron-builder-message'),

    consoleBtnMapCompressor: document.getElementById('console-btn-map-compressor'),
    settingsStopmodeSelect: document.getElementById('settings-stopmode-select'),
    settingsStopmodeSaveBtn: document.getElementById('settings-stopmode-save-btn'),
    settingsBtnOpenClone: document.getElementById('settings-btn-open-clone'),
    modalRustWipe: document.getElementById('modal-rust-wipe'),
    modalRustWipeCloseX: document.getElementById('modal-rust-wipe-close-x'),
    modalRustWipeCancel: document.getElementById('modal-rust-wipe-cancel'),
    modalRustWipeSubmit: document.getElementById('modal-rust-wipe-submit'),
    modalRustWipeSubtitle: document.getElementById('modal-rust-wipe-subtitle'),
    modalCloneInstance: document.getElementById('modal-clone-instance'),
    modalCloneCloseX: document.getElementById('modal-clone-close-x'),
    modalCloneCancel: document.getElementById('modal-clone-cancel'),
    modalCloneSubmit: document.getElementById('modal-clone-submit'),
    modalCloneSubtitle: document.getElementById('modal-clone-subtitle'),
    cloneInstancePrefix: document.getElementById('clone-instance-prefix'),
    cloneInstanceSuffix: document.getElementById('clone-instance-suffix'),
    modalCloneMessage: document.getElementById('modal-clone-message'),
    cloudbackupEnabled: document.getElementById('cloudbackup-enabled'),
    cloudbackupRemote: document.getElementById('cloudbackup-remote'),
    cloudbackupPath: document.getElementById('cloudbackup-path'),
    cloudbackupSettingsForm: document.getElementById('cloudbackup-settings-form'),
    cloudbackupBtnSave: document.getElementById('cloudbackup-btn-save'),
    cloudbackupBtnSync: document.getElementById('cloudbackup-btn-sync'),
    cloudbackupMessage: document.getElementById('cloudbackup-message')
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
    if (el.filterTagSelect) el.filterTagSelect.addEventListener('change', renderServersGrid);
    el.btnRefreshDashboard.addEventListener('click', refreshDashboard);

    if (el.btnBulkStart) el.btnBulkStart.addEventListener('click', () => runBulkAction('start'));
    if (el.btnBulkStop) el.btnBulkStop.addEventListener('click', () => runBulkAction('stop'));
    if (el.btnBulkRestart) el.btnBulkRestart.addEventListener('click', () => runBulkAction('restart'));
    if (el.btnBulkUpdate) el.btnBulkUpdate.addEventListener('click', () => runBulkAction('update'));
    if (el.modalPlayersClose) el.modalPlayersClose.addEventListener('click', closePlayersModal);
    if (el.cronBuilderForm) el.cronBuilderForm.addEventListener('submit', handleCronBuilderSubmit);
    
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
    if (el.consoleBtnCheckUpdate) el.consoleBtnCheckUpdate.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'check-update'));
    el.consoleBtnUpdateLgsm.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'update-lgsm'));
    el.consoleBtnForceUpdate.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'force-update'));
    if (el.consoleBtnModsInstall) el.consoleBtnModsInstall.addEventListener('click', () => {
        const server = state.servers.find(s => s.id === state.selectedConsoleServer);
        if (server) openModsModal(server);
    });
    if (el.consoleBtnModsUpdate) el.consoleBtnModsUpdate.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'mods-update'));
    if (el.consoleBtnFastdl) el.consoleBtnFastdl.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'fastdl'));
    if (el.consoleBtnPostdetails) el.consoleBtnPostdetails.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'postdetails'));
    el.consoleBtnTestAlert.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'test-alert'));
    if (el.consoleBtnMapCompressor) el.consoleBtnMapCompressor.addEventListener('click', () => runServerAction(state.selectedConsoleServer, 'map-compressor'));
    
    el.consoleModeTmux.addEventListener('click', () => switchConsoleMode('tmux'));
    el.consoleModeLog.addEventListener('click', () => switchConsoleMode('log'));
    if (el.consoleModeScript) el.consoleModeScript.addEventListener('click', () => switchConsoleMode('script'));
    if (el.consoleModeGame) el.consoleModeGame.addEventListener('click', () => switchConsoleMode('game'));
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
    watchSettingsWriteLock();
    if (el.settingsServerTagSaveBtn) el.settingsServerTagSaveBtn.addEventListener('click', saveServerTagFromSettings);
    if (el.settingsStopmodeSaveBtn) el.settingsStopmodeSaveBtn.addEventListener('click', saveServerStopModeFromSettings);
    if (el.settingsBtnOpenClone) el.settingsBtnOpenClone.addEventListener('click', () => {
        const srv = state.servers.find(s => s.id === el.settingsServerSelect.value);
        if (srv) openCloneModal(srv);
    });
    el.settingsToolsBtnSystemd.addEventListener('click', installSystemdService);
    el.settingsToolsBtnCron.addEventListener('click', installCronjobs);
    
    // Settings sub-tab navigation
    const settingsSubtabs = ['general', 'server', 'cron', 'alerts', 'security', 'nodes'];
    settingsSubtabs.forEach(tab => {
        const btn = document.getElementById(`settings-tab-btn-${tab}`);
        if (btn) {
            btn.addEventListener('click', () => switchSettingsSubtab(tab));
        }
    });
    initNodesPanel();

    // Backups listeners
    el.backupsServerSelect.addEventListener('change', handleBackupsServerChange);
    el.backupsBtnCreate.addEventListener('click', handleBackupsBtnCreateClick);
    el.backupsBtnUpload.addEventListener('click', () => el.backupsInputUpload.click());
    el.backupsInputUpload.addEventListener('change', handleBackupsUpload);
    el.backupsSettingsForm.addEventListener('submit', saveBackupSettings);
    if (el.cloudbackupSettingsForm) el.cloudbackupSettingsForm.addEventListener('submit', handleCloudBackupSave);
    if (el.cloudbackupBtnSync) el.cloudbackupBtnSync.addEventListener('click', handleCloudBackupSync);

    // Rust Wipe & Clone Modals listeners
    if (el.modalRustWipeCloseX) el.modalRustWipeCloseX.addEventListener('click', () => el.modalRustWipe.classList.add('hidden'));
    if (el.modalRustWipeCancel) el.modalRustWipeCancel.addEventListener('click', () => el.modalRustWipe.classList.add('hidden'));
    if (el.modalRustWipeSubmit) el.modalRustWipeSubmit.addEventListener('click', handleRustWipeSubmit);
    if (el.modalCloneCloseX) el.modalCloneCloseX.addEventListener('click', () => el.modalCloneInstance.classList.add('hidden'));
    if (el.modalCloneCancel) el.modalCloneCancel.addEventListener('click', () => el.modalCloneInstance.classList.add('hidden'));
    if (el.modalCloneSubmit) el.modalCloneSubmit.addEventListener('click', handleCloneSubmit);
    
    // Alerts listeners
    el.alertsSettingsForm.addEventListener('submit', saveAlertSettings);
    
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
    initGridColumns();
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
        const hasFiles = state.currentUser.permissions && state.currentUser.permissions.includes('files');
        const hasConsole = state.currentUser.permissions && state.currentUser.permissions.includes('console');
        if (viewName === 'console' && !isAdmin && !hasConsole) {
            window.location.hash = '#dashboard';
            return;
        }
        if (viewName === 'users' || viewName === 'installer' || viewName === 'audit') {
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
        if (viewName === 'files') {
            if (!isAdmin && !hasFiles) {
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
    document.querySelectorAll('.app-view').forEach(v => v.classList.add('hidden'));
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
        // Fresh statuses: the console buttons and the input depend on them.
        fetchServers().then(() => {
            if (state.lastActiveView !== 'console') return;
            populateConsoleDropdown();
            if (state.selectedConsoleServer) {
                startConsolePolling();
            }
        });
    } else if (viewName === 'users') {
        loadUsersList();
    } else if (viewName === 'settings') {
        loadSettingsInfo();
    } else if (viewName === 'installer') {
        loadGamesList();
    } else if (viewName === 'backups') {
        ensureServersLoaded().then(() => {
            populateBackupsDropdown();
        });
    } else if (viewName === 'files') {
        ensureServersLoaded().then(() => {
            updateFilesServerSelect();
            if (!currentFilesServer && state.servers && state.servers.length > 0) {
                currentFilesServer = state.servers[0].id;
                if (el.filesServerSelect) el.filesServerSelect.value = currentFilesServer;
            }
            applyFilesUploadLock();
            loadFilesTree();
        });
    } else if (viewName === 'audit') {
        loadAuditLogs();
    }
}

async function ensureServersLoaded() {
    if (!state.servers || state.servers.length === 0) await fetchServers();
}

// fetchServers reloads the server list; an error answer never replaces it.
async function fetchServers() {
    try {
        const res = await apiFetch('/api/servers');
        if (!res.ok) return false;
        const list = await res.json();
        if (!Array.isArray(list)) return false;
        state.servers = list;
        return true;
    } catch (e) {
        console.error('Failed to fetch servers:', e);
        return false;
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
            // Logged in! The role must be known before routing checks it.
            await hideLogin();
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
    clearInterval(state.nodesInterval);
    state.nodesInterval = null;
}

async function hideLogin() {
    el.loginContainer.classList.add('hidden');
    el.appContainer.classList.remove('hidden');
    
    try {
        const res = await fetch('/api/system/info');
        if (res.status === 200) {
            const data = await res.json();
            state.currentUser = data.user;
            if (data.csrf_token) state.csrfToken = data.csrf_token;
            applyUpdateStatus(data.update);
            startUpdateStatusPolling();
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

            const menuFilesTab = document.getElementById('menu-files-tab');
            if (menuFilesTab) {
                if (hasPerm('files')) {
                    menuFilesTab.classList.remove('hidden');
                } else {
                    menuFilesTab.classList.add('hidden');
                    if (window.location.hash === '#files') {
                        window.location.hash = '#dashboard';
                    }
                }
            }

            const menuConsoleTab = document.getElementById('menu-console-tab');
            if (menuConsoleTab) {
                menuConsoleTab.classList.toggle('hidden', !hasPerm('console'));
                if (!hasPerm('console') && window.location.hash === '#console') {
                    window.location.hash = '#dashboard';
                }
            }

            applySettingsPermissions();

            const menuAuditTab = document.getElementById('menu-audit-tab');
            if (menuAuditTab) {
                if (state.currentUser && state.currentUser.role === 'admin') {
                    menuAuditTab.classList.remove('hidden');
                } else {
                    menuAuditTab.classList.add('hidden');
                    if (window.location.hash === '#audit') {
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
        
        const data = await res.json().catch(() => ({}));
        if (res.status === 200 && data.status === '2fa_required') {
            showSecondFactorStep(data.challenge);
        } else if (res.status === 200) {
            completeLogin();
        } else {
            el.loginError.textContent = data.error || t('login-invalid');
            el.loginError.style.display = 'block';
        }
    } catch (err) {
        el.loginError.textContent = 'Serververbindung fehlgeschlagen';
        el.loginError.style.display = 'block';
    }
}

async function completeLogin() {
    el.passwordInput.value = '';
    await hideLogin();
    window.location.hash = '#dashboard';
    handleRouting();
}

// The challenge is bound to the account whose password was just verified; the second step does
// not send a username.
let loginChallengeToken = '';

function showSecondFactorStep(challenge) {
    loginChallengeToken = challenge;
    el.loginForm.classList.add('hidden');
    const form = document.getElementById('login-2fa-form');
    const err = document.getElementById('login-2fa-error');
    err.style.display = 'none';
    form.classList.remove('hidden');
    const input = document.getElementById('login-2fa-code');
    input.value = '';
    input.focus();
}

function resetLoginSteps() {
    loginChallengeToken = '';
    document.getElementById('login-2fa-form').classList.add('hidden');
    el.loginForm.classList.remove('hidden');
    el.passwordInput.value = '';
    el.passwordInput.focus();
}

async function handleSecondFactorLogin(e) {
    e.preventDefault();
    const err = document.getElementById('login-2fa-error');
    err.style.display = 'none';
    const code = document.getElementById('login-2fa-code').value.trim();
    try {
        const res = await fetch('/api/auth/login/2fa', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ challenge: loginChallengeToken, code })
        });
        const data = await res.json().catch(() => ({}));
        if (res.status === 200) {
            loginChallengeToken = '';
            document.getElementById('login-2fa-form').classList.add('hidden');
            el.loginForm.classList.remove('hidden');
            completeLogin();
            return;
        }
        if (res.status === 401 && /expired/i.test(data.error || '')) {
            resetLoginSteps();
            el.loginError.textContent = t('login-2fa-expired');
            el.loginError.style.display = 'block';
            return;
        }
        err.textContent = res.status === 429 ? (data.error || t('login-2fa-invalid')) : t('login-2fa-invalid');
        err.style.display = 'block';
        document.getElementById('login-2fa-code').select();
    } catch (ex) {
        err.textContent = t('connection-error');
        err.style.display = 'block';
    }
}

document.getElementById('login-2fa-form').addEventListener('submit', handleSecondFactorLogin);
document.getElementById('login-2fa-back').addEventListener('click', resetLoginSteps);

// -------------------------------------------------------------
// Two-factor authentication (own account, Settings > General)
// -------------------------------------------------------------

let twoFactorCodes = [];

function twoFactorShow(section) {
    ['off', 'setup', 'codes', 'on'].forEach(name => {
        document.getElementById(`twofa-${name}`).classList.toggle('hidden', name !== section);
    });
}

function twoFactorMessage(text, kind = '') {
    const m = document.getElementById('twofa-message');
    m.textContent = text || '';
    m.className = `info-message ${kind}`;
}

async function loadTwoFactorStatus() {
    const status = document.getElementById('twofa-status');
    if (!status) return;
    try {
        const res = await apiFetch('/api/account/2fa');
        if (res.status !== 200) return;
        const data = await res.json();
        if (data.enabled) {
            status.textContent = t('twofa-status-on').replace('{n}', data.recovery_codes_left);
            status.className = 'info-value text-success';
        } else {
            status.textContent = t('twofa-status-off');
            status.className = 'info-value text-muted';
        }
        if (!document.getElementById('twofa-codes').classList.contains('hidden')) return; // codes still shown
        twoFactorShow(data.enabled ? 'on' : 'off');
    } catch (e) {
        console.error('2FA status failed:', e);
    }
}

async function twoFactorPost(url, body) {
    const res = await apiFetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body)
    });
    const data = await res.json().catch(() => ({}));
    if (res.status !== 200) throw new Error(data.error || t('status-error'));
    return data;
}

function showRecoveryCodes(codes) {
    twoFactorCodes = codes;
    const list = document.getElementById('twofa-codes-list');
    list.innerHTML = '';
    codes.forEach(code => {
        const li = document.createElement('li');
        li.textContent = code;
        list.appendChild(li);
    });
    twoFactorShow('codes');
}

function initTwoFactorSettings() {
    if (!document.getElementById('settings-2fa-card')) return;

    document.getElementById('twofa-btn-start').addEventListener('click', async () => {
        const pw = document.getElementById('twofa-setup-password');
        try {
            const data = await twoFactorPost('/api/account/2fa/setup', { password: pw.value });
            pw.value = '';
            document.getElementById('twofa-qr').src = data.qr;
            document.getElementById('twofa-secret').textContent = data.secret.replace(/(.{4})/g, '$1 ').trim();
            document.getElementById('twofa-setup-code').value = '';
            twoFactorMessage('');
            twoFactorShow('setup');
        } catch (e) {
            twoFactorMessage(e.message, 'text-danger');
        }
    });

    document.getElementById('twofa-btn-confirm').addEventListener('click', async () => {
        try {
            const data = await twoFactorPost('/api/account/2fa/enable', { code: document.getElementById('twofa-setup-code').value });
            document.getElementById('twofa-qr').removeAttribute('src');
            document.getElementById('twofa-secret').textContent = '';
            twoFactorMessage(t('twofa-enabled'), 'text-success');
            showRecoveryCodes(data.recovery_codes);
            loadTwoFactorStatus();
        } catch (e) {
            twoFactorMessage(e.message, 'text-danger');
        }
    });

    document.getElementById('twofa-btn-cancel').addEventListener('click', () => {
        document.getElementById('twofa-qr').removeAttribute('src');
        document.getElementById('twofa-secret').textContent = '';
        twoFactorMessage('');
        twoFactorShow('off');
    });

    const manageInputs = () => ({
        password: document.getElementById('twofa-manage-password').value,
        code: document.getElementById('twofa-manage-code').value.trim()
    });
    const clearManageInputs = () => {
        document.getElementById('twofa-manage-password').value = '';
        document.getElementById('twofa-manage-code').value = '';
    };

    document.getElementById('twofa-btn-new-codes').addEventListener('click', async () => {
        try {
            const data = await twoFactorPost('/api/account/2fa/recovery-codes', manageInputs());
            clearManageInputs();
            twoFactorMessage(t('twofa-new-codes'), 'text-success');
            showRecoveryCodes(data.recovery_codes);
            loadTwoFactorStatus();
        } catch (e) {
            twoFactorMessage(e.message, 'text-danger');
        }
    });

    document.getElementById('twofa-btn-disable').addEventListener('click', async () => {
        if (!confirm(t('twofa-confirm-disable'))) return;
        try {
            await twoFactorPost('/api/account/2fa/disable', manageInputs());
            clearManageInputs();
            twoFactorMessage(t('twofa-disabled'), 'text-success');
            twoFactorShow('off');
            loadTwoFactorStatus();
        } catch (e) {
            twoFactorMessage(e.message, 'text-danger');
        }
    });

    document.getElementById('twofa-btn-copy').addEventListener('click', async () => {
        try {
            await navigator.clipboard.writeText(twoFactorCodes.join('\n'));
            twoFactorMessage(t('twofa-copied'), 'text-success');
        } catch (e) {
            twoFactorMessage(t('twofa-copy-failed'), 'text-danger');
        }
    });

    document.getElementById('twofa-btn-download').addEventListener('click', () => {
        const user = state.currentUser ? state.currentUser.username : 'user';
        const text = `LinuxGSM Dashboard - ${t('twofa-file-title')} (${user}@${location.host})\n\n` + twoFactorCodes.join('\n') + '\n';
        const link = document.createElement('a');
        link.href = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
        link.download = `lgsm-dashboard-recovery-codes-${user}.txt`;
        link.click();
        setTimeout(() => URL.revokeObjectURL(link.href), 1000);
    });

    document.getElementById('twofa-btn-done').addEventListener('click', () => {
        twoFactorCodes = [];
        document.getElementById('twofa-codes-list').innerHTML = '';
        twoFactorMessage('');
        twoFactorShow('on');
        loadTwoFactorStatus();
    });
}

initTwoFactorSettings();

// Administrator: remove 2FA from an account whose owner lost the device.
async function resetUserTwoFactor(username) {
    if (!confirm(t('twofa-confirm-reset').replace('{user}', username))) return;
    try {
        const res = await apiFetch('/api/admin/users/2fa-reset', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username })
        });
        const data = await res.json().catch(() => ({}));
        if (res.status !== 200) throw new Error(data.error || t('status-error'));
        loadUsersList();
    } catch (e) {
        alert(e.message);
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
        if (!(await fetchServers())) return;
        updateStatsOverview();
        renderServersGrid();
    } catch (e) {
        console.error(e);
    } finally {
        setTimeout(() => el.btnRefreshDashboard.classList.remove('animate-spin'), 600);
    }
}

document.addEventListener('click', (e) => {
    document.querySelectorAll('.sc-menu[open]').forEach(menu => {
        if (!menu.contains(e.target)) menu.removeAttribute('open');
    });
});

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
    
    // 1. Populate Tag Filter dropdown options
    if (el.filterTagSelect) {
        const currentTag = el.filterTagSelect.value;
        const tags = new Set();
        state.servers.forEach(s => {
            if (s.tag) tags.add(s.tag);
        });
        
        let tagOptionsHtml = `<option value="">${t('filter-all-tags', 'Alle Clusters / Tags', 'Alle Clusters / Tags')}</option>`;
        tags.forEach(t => {
            tagOptionsHtml += `<option value="${escapeHtml(t)}">${escapeHtml(t)}</option>`;
        });
        el.filterTagSelect.innerHTML = tagOptionsHtml;
        if (currentTag && tags.has(currentTag)) {
            el.filterTagSelect.value = currentTag;
        }
    }
    
    const filter = el.searchServers.value.toLowerCase();
    const tagFilter = el.filterTagSelect ? el.filterTagSelect.value : '';
    
    const filtered = state.servers.filter(s => {
        const matchesText = s.name.toLowerCase().includes(filter) || 
                            s.user.toLowerCase().includes(filter) ||
                            s.script.toLowerCase().includes(filter);
        const matchesTag = !tagFilter || s.tag === tagFilter;
        return matchesText && matchesTag;
    });
    
    if (filtered.length === 0) {
        el.serversContainer.innerHTML = `
            <div class="empty-state">
                <p>${t('no-servers-found')}</p>
                <a href="#installer" class="btn btn-primary mt-4">${t('inst-btn-install')}</a>
            </div>
        `;
        return;
    }
    
    filtered.forEach(server => {
        const card = createServerCard(server);
        el.serversContainer.appendChild(card);
    });
    updateBulkToolbarState();
}

// Renders the grid but keeps inputs/focus and updates values smoothly
function renderServersGridQuiet() {
    const filter = el.searchServers.value.toLowerCase();
    const tagFilter = el.filterTagSelect ? el.filterTagSelect.value : '';

    const filtered = state.servers.filter(s => {
        const matchesText = s.name.toLowerCase().includes(filter) || s.user.toLowerCase().includes(filter) || s.script.toLowerCase().includes(filter);
        const matchesTag = !tagFilter || s.tag === tagFilter;
        return matchesText && matchesTag;
    });

    // A server that appeared or vanished (deleted elsewhere, node removed) needs a full redraw,
    // also for the tag filter.
    const shown = [...el.serversContainer.querySelectorAll('.server-card')].map(c => c.id).sort().join('|');
    const wanted = filtered.map(s => `server-card-${s.id}`).sort().join('|');
    if (shown !== wanted) {
        renderServersGrid();
        return;
    }

    filtered.forEach(server => {
        const card = document.getElementById(`server-card-${server.id}`);
        if (!card) return;
        const menuOpen = card.querySelector('.sc-menu[open]');
        if (card.dataset.status !== server.status && !menuOpen) {
            // Status changed: the available actions differ, so rebuild this card only.
            const checked = card.querySelector('.bulk-server-checkbox')?.checked;
            const fresh = createServerCard(server);
            card.replaceWith(fresh);
            if (checked) fresh.querySelector('.bulk-server-checkbox').checked = true;
            return;
        }
        fillServerCardStats(card, server);
    });
}

function hasUserPermission(permission) {
    if (!state.currentUser) return false;
    if (state.currentUser.role === 'admin') return true;
    return state.currentUser.permissions && state.currentUser.permissions.includes(permission);
}

function fastdlUrl(server) {
    return `${window.location.protocol}//${window.location.host}/fastdl/${encodeURIComponent(server.id)}/`;
}

// loadFastDLInfo fills the FastDL panel: who serves the files (dashboard, the node itself or
// nobody), the resulting URL, and for administrators the choice.
async function loadFastDLInfo(server) {
    const box = document.getElementById('game-fastdl-info');
    if (!box || box.dataset.server !== server.id) return;
    let info = { mode: 'dashboard' };
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(server.id)}/fastdl`);
        if (res.ok) info = await res.json();
    } catch (e) {
        // keep the default
    }
    if (box.dataset.server !== server.id) return;
    // For servers on nodes the answer comes from the node: only known modes are used.
    if (!['dashboard', 'node', 'off'].includes(info.mode)) info.mode = 'dashboard';
    let url = '';
    let note = '';
    if (info.mode === 'dashboard') url = fastdlUrl(server);
    else if (info.mode === 'node') {
        url = info.node_url || '';
        if (!url) note = t('fastdl-node-no-url');
    } else note = t('fastdl-off');

    const isAdmin = state.currentUser && state.currentUser.role === 'admin';
    const writable = !isReadOnlyNodeServer(server) && !server.node_offline;
    const opt = (value) => `<option value="${value}" ${info.mode === value ? 'selected' : ''}>${t('fastdl-mode-' + value)}</option>`;
    const select = isAdmin && writable
        ? `<select class="form-control form-control-sm" style="max-width: 16rem;" onchange="setFastDLMode(${jsArg(server.id)}, this.value)" aria-label="${t('fastdl-mode-label')}">
               ${opt('dashboard')}${server.node ? opt('node') : ''}${opt('off')}
           </select>`
        : `<span class="text-muted" style="font-size: 0.8rem;">${t('fastdl-mode-' + (info.mode || 'dashboard'))}</span>`;
    const urlPart = url
        ? `<span style="font-size: 0.8rem; color: var(--text-secondary);"><strong>FastDL URL:</strong> <code class="font-mono" style="color: var(--primary);">${escapeHtml(url)}</code></span>
           <button class="btn btn-secondary btn-xs" onclick="navigator.clipboard.writeText(${jsArg(url)}); alert(t('fastdl-copied'));">${t('nodes-copy')}</button>`
        : `<span class="text-muted" style="font-size: 0.8rem;">${escapeHtml(note)}</span>`;
    box.innerHTML = `<span style="font-size: 0.8rem; color: var(--text-secondary);">${t('fastdl-mode-label')}:</span> ${select} ${urlPart}`;
}

async function setFastDLMode(serverId, mode) {
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(serverId)}/fastdl`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ mode }),
        });
        if (!res.ok) {
            const data = await res.json().catch(() => ({}));
            alert(`${t('nodes-error')}: ${data.error || res.status}`);
        }
    } catch (e) {
        alert(t('connection-error'));
    }
    const server = state.servers.find(s => s.id === serverId);
    if (server) loadFastDLInfo(server);
}

// serverWriteLocked reports whether nothing may be changed on a server (by ID): it runs on a
// read-only node or its node is offline. The node refuses such changes anyway; the views hide
// or disable the controls so the user does not run into that error.
function serverWriteLocked(serverId) {
    const server = state.servers.find(s => s.id === serverId);
    return !!(server && (server.node_offline || isReadOnlyNodeServer(server)));
}

function serverWriteLockedText(serverId) {
    const server = state.servers.find(s => s.id === serverId);
    return server && server.node_offline ? t('card-node-offline') : t('card-node-read-only');
}

// isReadOnlyNodeServer reports whether a server runs on a node that only allows reading plus
// start, stop and restart.
function isReadOnlyNodeServer(server) {
    return !!(server && server.node && server.node_mode !== 'read-write');
}

// serverLabel names a server in dropdowns; servers of nodes carry the node name.
function serverLabel(server) {
    const base = `${server.name} (${server.user})`;
    return server.node ? `${base} · ${server.node_name || server.node}` : base;
}

function serverStatusInfo(server) {
    if (server.status === 'running') return { cls: 'is-running', dot: 'status-online', text: t('status-online'), busy: false };
    if (server.status === 'installing') return { cls: 'is-busy', dot: 'status-busy', text: t('status-installing'), busy: true };
    if (server.status === 'updating') return { cls: 'is-busy', dot: 'status-busy', text: t('status-updating'), busy: true };
    return { cls: 'is-stopped', dot: 'status-offline', text: t('status-offline'), busy: false };
}

function formatSize(bytes) {
    if (!bytes || bytes <= 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    const v = bytes / Math.pow(1024, i);
    return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

function diskSummary(disk) {
    if (!disk) return { text: '-', title: t('card-disk-pending') };
    const measured = disk.updated && !String(disk.updated).startsWith('0001-');
    if (!measured) {
        return disk.scanning ? { text: t('card-measuring'), title: t('card-disk-scanning') } : { text: '-', title: t('card-disk-pending') };
    }
    const parts = [];
    if (disk.backups) parts.push(`${t('card-disk-backups')}: ${formatSize(disk.backups)}`);
    if (disk.fs_total) parts.push(`${t('card-disk-free')}: ${formatSize(disk.fs_free)} / ${formatSize(disk.fs_total)}`);
    if (disk.shared) parts.push(t('card-disk-shared'));
    if (disk.scanning) parts.push(t('card-disk-scanning'));
    parts.push(`${t('card-disk-updated')}: ${new Date(disk.updated).toLocaleTimeString()}`);
    if (disk.error) {
        // Incomplete measurement: show what was counted, marked as lower bound.
        parts.unshift(`${t('card-disk-error')}: ${disk.error}`);
        return { text: disk.used ? `>= ${formatSize(disk.used)}` : 'n/a', title: parts.join(' | ') };
    }
    return { text: formatSize(disk.used), title: parts.join(' | ') };
}

// Updates the values of a card without rebuilding it.
function fillServerCardStats(card, server) {
    const set = (field, text, title) => {
        const node = card.querySelector(`[data-field="${field}"]`);
        if (!node) return;
        node.textContent = text;
        if (title !== undefined) node.title = title;
    };
    const running = server.status === 'running';
    const q = server.query_data || {};
    const online = running && q.online;

    set('players', online ? `${q.num_players || 0} / ${q.max_players || 0}` : '-', online ? '' : (running ? t('card-no-query') : ''));
    set('map', online && q.map ? q.map : '-', online && q.name ? q.name : '');
    set('cpu', running ? `${(server.cpu || 0).toFixed(1)} %` : '-');
    set('ram', running ? `${(server.ram || 0).toFixed(1)} GB` : '-');
    const disk = diskSummary(server.disk);
    set('disk', disk.text, disk.title);

    const playersBtn = card.querySelector('[data-field="players"]');
    if (playersBtn) playersBtn.disabled = !online;

    const busy = serverStatusInfo(server).busy;
    card.querySelectorAll('.btn-server-action').forEach(btn => { btn.disabled = busy; });
}

function createServerCard(server) {
    const status = serverStatusInfo(server);
    const card = document.createElement('article');
    card.id = `server-card-${server.id}`;
    card.className = `server-card ${status.cls}${server.node_offline ? ' is-node-offline' : ''}`;
    card.dataset.status = server.status;

    const id = jsArg(server.id);
    // Servers of an offline node show their last known state; nothing can be run on them.
    const dis = status.busy || server.node_offline ? 'disabled' : '';
    // A read-only node allows reading plus start, stop and restart; it refuses everything else.
    const readOnlyNode = isReadOnlyNodeServer(server);
    const isAdmin = state.currentUser && state.currentUser.role === 'admin';
    const canStart = hasUserPermission('start');
    const canStop = hasUserPermission('stop');
    const canRestart = hasUserPermission('restart');
    const canConsole = hasUserPermission('console');
    const canConfig = hasUserPermission('config');
    const canBackup = hasUserPermission('backup');
    const isRust = (server.game || '').toLowerCase() === 'rust' || server.script === 'rustserver';

    // Primary actions: the things done every day.
    let primary = '';
    if (server.status === 'running' && canStop) {
        primary += `<button class="btn btn-sm btn-danger btn-server-action" onclick="runServerAction(${id}, 'stop')" ${dis}>${t('btn-stop')}</button>`;
    } else if (server.status !== 'running' && canStart) {
        primary += `<button class="btn btn-sm btn-success btn-server-action" onclick="runServerAction(${id}, 'start')" ${dis}>${t('btn-start')}</button>`;
    }
    if (canRestart) {
        primary += `<button class="btn btn-sm btn-secondary btn-server-action" onclick="runServerAction(${id}, 'restart')" ${dis}>${t('btn-restart')}</button>`;
    }
    if (canConsole) {
        primary += `<button class="btn btn-sm btn-secondary btn-server-action" onclick="openConsole(${id})" ${dis}>${t('btn-console')}</button>`;
    }

    // Everything else lives in a menu.
    const menuItems = [];
    if (canConfig) menuItems.push(`<button class="btn-server-action" onclick="openConfigEditor(${id})" ${dis}>${t('btn-configs')}</button>`);
    if (canBackup) {
        if (!readOnlyNode) menuItems.push(`<button class="btn-server-action" onclick="runServerAction(${id}, 'backup')" ${dis}>${t('btn-backup')}</button>`);
        menuItems.push(`<button class="btn-server-action" onclick="runServerAction(${id}, 'details')" ${dis}>${t('btn-details')}</button>`);
    }
    if (isAdmin) {
        menuItems.push('<hr>');
        if (!readOnlyNode) {
            menuItems.push(`<button class="btn-server-action" onclick="runServerAction(${id}, 'update')" ${dis}>${t('btn-update')}</button>`);
            menuItems.push(`<button class="btn-server-action" onclick="runServerAction(${id}, 'validate')" ${dis}>${t('btn-validate')}</button>`);
        }
        menuItems.push(`<button onclick="checkServerPorts(${id})" ${server.node_offline ? 'disabled' : ''}>${t('btn-portcheck')}</button>`);
        if (isRust && !readOnlyNode) {
            menuItems.push(`<button class="btn-server-action" onclick="openRustWipeModal(state.servers.find(s => s.id === ${id}))" ${dis}>${t('btn-wipe-server')}</button>`);
        }
        menuItems.push(`<button onclick="editServerTag(${id}, ${jsArg(server.tag || '')})">${t('card-edit-tag')}</button>`);
        if (!readOnlyNode) {
            menuItems.push('<hr>');
            menuItems.push(`<button class="is-danger btn-server-action" onclick="openDeleteServerModal(${id})" ${dis}>${t('btn-delete-server')}</button>`);
        }
    }
    const menu = menuItems.length ? `
        <details class="sc-menu">
            <summary class="btn btn-sm btn-ghost" title="${t('card-more')}">${t('card-more')}</summary>
            <div class="sc-menu-list">${menuItems.join('')}</div>
        </details>` : '';

    const tag = server.tag ? `<span class="tag-chip">${escapeHtml(server.tag)}</span>` : '';
    const nodeTitle = readOnlyNode ? ` title="${escapeHtml(t('card-node-read-only'))}"` : '';
    const nodeChip = server.node ? `<span class="node-chip${readOnlyNode ? ' is-read-only' : ''}"${nodeTitle}>${escapeHtml(server.node_name || server.node)}</span>` : '';
    const statusDot = server.node_offline ? 'status-offline' : status.dot;
    const statusText = server.node_offline ? t('card-node-offline') : status.text;
    const dirTitle = server.dir ? ` title="${escapeHtml(server.dir)}"` : '';

    card.innerHTML = `
        <header class="sc-head">
            <input type="checkbox" class="bulk-server-checkbox" data-id="${escapeHtml(server.id)}" onchange="updateBulkToolbarState()" aria-label="${t('card-select')}">
            <div class="sc-title">
                <h3>${escapeHtml(server.name)}</h3>
                <div class="sc-meta"${dirTitle}>
                    <span class="mono">${escapeHtml(server.script)}</span>
                    <span>${escapeHtml(server.user)}</span>
                    ${server.port ? `<span class="mono">:${escapeHtml(server.port)}</span>` : ''}
                    ${nodeChip}
                    ${tag}
                </div>
            </div>
            <span class="status-pill"><span class="status-dot ${statusDot}"></span>${statusText}</span>
        </header>
        <dl class="sc-stats">
            <div><dt>${t('card-players')}</dt><dd><button type="button" class="link-btn" data-field="players" onclick="openPlayersModal(${id})">-</button></dd></div>
            <div><dt>${t('card-map')}</dt><dd data-field="map">-</dd></div>
            <div><dt>CPU</dt><dd data-field="cpu">-</dd></div>
            <div><dt>RAM</dt><dd data-field="ram">-</dd></div>
            <div><dt>${t('card-disk')}</dt><dd data-field="disk">-</dd></div>
        </dl>
        <footer class="sc-actions">
            <div class="sc-primary">${primary}</div>
            ${menu}
        </footer>
    `;
    fillServerCardStats(card, server);
    return card;
}

// -------------------------------------------------------------
// Action Logs Live Stream Handler
// extra carries the answers a command needs (mod for mods-install/-remove, password for the TS3
// change-password).
function runServerAction(serverId, action, extra = {}) {
    const tAction = t('btn-' + action) || action.toUpperCase();
    runStreamModal({
        title: t('modal-stream-title-run').replace('{action}', tAction).replace('{server}', serverId),
        runningText: t('status-running-action'),
        url: `/api/servers/${encodeURIComponent(serverId)}/action`,
        body: { ...extra, action, lang: state.language },
        errorText: t('modal-stream-err-run'),
        onExit: () => {
            // If in console tab, refresh console button states
            if (state.lastActiveView === 'console' && state.selectedConsoleServer === serverId) {
                fetchServers().then(updateConsoleViewButtons);
            }
        }
    });
}

// -------------------------------------------------------------
// Live Console View
// -------------------------------------------------------------

function populateConsoleDropdown() {
    // The server chosen on a card (openConsole) wins over the previous selection.
    const current = state.selectedConsoleServer || el.consoleServerSelect.value;
    
    el.consoleServerSelect.innerHTML = `<option value="">${t('console-select-default')}</option>`;
    
    state.servers.forEach(server => {
        const opt = document.createElement('option');
        opt.value = server.id;
        opt.textContent = serverLabel(server);
        el.consoleServerSelect.appendChild(opt);
    });
    
    if (current && state.servers.find(s => s.id === current)) {
        el.consoleServerSelect.value = current;
        if (state.selectedConsoleServer !== current) el.terminalOutput.innerHTML = '';
        state.selectedConsoleServer = current;
    } else {
        state.selectedConsoleServer = '';
    }
    updateConsoleViewButtons();
}

function openConsole(serverId) {
    if (state.selectedConsoleServer !== serverId) el.terminalOutput.innerHTML = '';
    state.selectedConsoleServer = serverId;
    // The hashchange event routes; only an unchanged hash needs routing here.
    if (window.location.hash === '#console') handleRouting();
    else window.location.hash = '#console';
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
        if (el.consoleBtnCheckUpdate) el.consoleBtnCheckUpdate.disabled = true;
        el.consoleBtnUpdateLgsm.disabled = true;
        el.consoleBtnForceUpdate.disabled = true;
        if (el.consoleBtnModsInstall) el.consoleBtnModsInstall.disabled = true;
        if (el.consoleBtnModsUpdate) el.consoleBtnModsUpdate.disabled = true;
        if (el.consoleBtnFastdl) el.consoleBtnFastdl.disabled = true;
        if (el.consoleBtnPostdetails) el.consoleBtnPostdetails.disabled = true;
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
    if (el.consoleBtnCheckUpdate) el.consoleBtnCheckUpdate.disabled = isBusy;
    el.consoleBtnUpdateLgsm.disabled = isBusy;
    el.consoleBtnForceUpdate.disabled = isBusy;
    if (el.consoleBtnModsInstall) el.consoleBtnModsInstall.disabled = isBusy;
    if (el.consoleBtnModsUpdate) el.consoleBtnModsUpdate.disabled = isBusy;
    if (el.consoleBtnFastdl) el.consoleBtnFastdl.disabled = isBusy;
    if (el.consoleBtnPostdetails) el.consoleBtnPostdetails.disabled = isBusy;
    el.consoleBtnTestAlert.disabled = isBusy;
    el.consoleInputField.disabled = !isRunning || isBusy;
    el.consoleInputSubmit.disabled = !isRunning || isBusy;
    
    const canBackup = hasUserPermission('backup');
    const isAdmin = state.currentUser && state.currentUser.role === 'admin';
    
    // details prints the server passwords, so it needs the config permission like the editor.
    el.consoleBtnDetails.classList.toggle('hidden', !(isAdmin || hasUserPermission('config')));
    el.consoleBtnBackup.classList.toggle('hidden', !(isAdmin || canBackup));
    if (el.consoleBtnPostdetails) el.consoleBtnPostdetails.classList.toggle('hidden', !isAdmin);
    
    if (isAdmin) {
        el.consoleBtnValidate.classList.remove('hidden');
        if (el.consoleBtnCheckUpdate) el.consoleBtnCheckUpdate.classList.remove('hidden');
        el.consoleBtnUpdateLgsm.classList.remove('hidden');
        el.consoleBtnForceUpdate.classList.remove('hidden');
        if (el.consoleBtnModsInstall) el.consoleBtnModsInstall.classList.remove('hidden');
        if (el.consoleBtnModsUpdate) el.consoleBtnModsUpdate.classList.remove('hidden');
        if (el.consoleBtnFastdl) el.consoleBtnFastdl.classList.remove('hidden');
        el.consoleBtnTestAlert.classList.remove('hidden');
    } else {
        el.consoleBtnValidate.classList.add('hidden');
        if (el.consoleBtnCheckUpdate) el.consoleBtnCheckUpdate.classList.add('hidden');
        el.consoleBtnUpdateLgsm.classList.add('hidden');
        el.consoleBtnForceUpdate.classList.add('hidden');
        if (el.consoleBtnModsInstall) el.consoleBtnModsInstall.classList.add('hidden');
        if (el.consoleBtnModsUpdate) el.consoleBtnModsUpdate.classList.add('hidden');
        if (el.consoleBtnFastdl) el.consoleBtnFastdl.classList.add('hidden');
        el.consoleBtnTestAlert.classList.add('hidden');
    }
    // Only commands LinuxGSM offers for this game (validate needs SteamCMD, fastdl Source, ...).
    if (Array.isArray(server.commands)) {
        [[el.consoleBtnValidate, 'validate'], [el.consoleBtnCheckUpdate, 'check-update'], [el.consoleBtnForceUpdate, 'force-update'],
            [el.consoleBtnUpdateLgsm, 'update-lgsm'], [el.consoleBtnModsInstall, 'mods-install'], [el.consoleBtnModsUpdate, 'mods-update'],
            [el.consoleBtnFastdl, 'fastdl'], [el.consoleBtnMapCompressor, 'map-compressor']]
            .forEach(([btn, cmd]) => { if (btn && !server.commands.includes(cmd)) btn.classList.add('hidden'); });
        // consoleinteract="no": the game reads nothing typed into its console.
        if (!server.commands.includes('send')) {
            el.consoleInputField.disabled = true;
            el.consoleInputSubmit.disabled = true;
            if (el.consoleInputField.dataset.defaultPlaceholder === undefined) {
                el.consoleInputField.dataset.defaultPlaceholder = el.consoleInputField.placeholder;
            }
            el.consoleInputField.placeholder = t('console-no-input');
        } else if (el.consoleInputField.dataset.defaultPlaceholder !== undefined && !isReadOnlyNodeServer(server)) {
            el.consoleInputField.placeholder = el.consoleInputField.dataset.defaultPlaceholder;
        }
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

    // Servers on nodes: nothing works while the node is offline, and a read-only node only
    // allows start, stop and restart (the node refuses everything else anyway).
    if (server.node_offline || isReadOnlyNodeServer(server)) {
        const writes = [el.consoleBtnBackup, el.consoleBtnValidate, el.consoleBtnCheckUpdate, el.consoleBtnUpdateLgsm,
            el.consoleBtnForceUpdate, el.consoleBtnModsInstall, el.consoleBtnModsUpdate, el.consoleBtnFastdl,
            el.consoleBtnPostdetails, el.consoleBtnTestAlert, el.consoleInputField, el.consoleInputSubmit];
        writes.forEach(b => { if (b) b.disabled = true; });
        if (server.node_offline) {
            [el.consoleBtnStart, el.consoleBtnStop, el.consoleBtnRestart, el.consoleBtnConfig, el.consoleBtnDetails]
                .forEach(b => { if (b) b.disabled = true; });
            el.consoleStatusDot.className = 'status-dot status-offline';
            el.consoleStatusText.textContent = t('card-node-offline');
        } else {
            if (el.consoleInputField.dataset.defaultPlaceholder === undefined) {
                el.consoleInputField.dataset.defaultPlaceholder = el.consoleInputField.placeholder;
            }
            el.consoleInputField.placeholder = t('card-node-read-only');
        }
    } else if (el.consoleInputField.dataset.defaultPlaceholder !== undefined && !(Array.isArray(server.commands) && !server.commands.includes('send'))) {
        el.consoleInputField.placeholder = el.consoleInputField.dataset.defaultPlaceholder;
        delete el.consoleInputField.dataset.defaultPlaceholder;
    }

    renderConsoleGameActions(isReadOnlyNodeServer(server) || server.node_offline ? null : server);
}

function startConsolePolling() {
    stopConsolePolling();
    
    // Initial fetch
    fetchConsoleData();
    
    // Poll every 1.5 seconds
    state.consoleInterval = setInterval(fetchConsoleData, 1500);
    // The status decides which buttons work and whether commands can be sent.
    state.consoleStatusInterval = setInterval(() => {
        fetchServers().then(ok => { if (ok && state.lastActiveView === 'console') updateConsoleViewButtons(); });
    }, 5000);
}

function stopConsolePolling() {
    if (state.consoleInterval) {
        clearInterval(state.consoleInterval);
        state.consoleInterval = null;
    }
    clearInterval(state.consoleStatusInterval);
    state.consoleStatusInterval = null;
}

async function fetchConsoleData() {
    if (!state.selectedConsoleServer) return;
    const serverId = state.selectedConsoleServer, mode = state.consoleMode;
    
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(serverId)}/console?mode=${mode}`);
        const data = await res.json();
        // A late answer for another server or mode must not overwrite the terminal.
        if (serverId !== state.selectedConsoleServer || mode !== state.consoleMode) return;
        
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

async function openConfigEditor(serverId, selectPath) {
    const seq = ++state.configLoadSeq;
    state.activeConfigServer = serverId;
    state.activeConfigFile = null;
    state.activeConfigContent = '';
    state.parsedConfigItems = [];
    state.formBaseContent = '';
    state.configFileEntries = [];

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
    el.configFormBuilder.innerHTML = '';
    setConfigView('raw');

    el.modalConfig.classList.remove('hidden');
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/configs`);
        const files = await res.json();
        // Another server or file was opened while the list was loading.
        if (seq !== state.configLoadSeq) return;
        if (!res.ok || !Array.isArray(files)) throw new Error(files && files.error);

        el.configFilesList.innerHTML = '';
        if (files.length === 0) {
            el.configFilesList.innerHTML = `<li class="config-file-item text-muted">${t('config-no-files')}</li>`;
            return;
        }
        
        files.forEach(file => {
            const li = document.createElement('li');
            li.className = 'config-file-item';
            
            let layerBadge = '';
            if (file.layer === 'default') layerBadge = `<span class="badge badge-secondary" style="font-size:0.65rem; margin-left:auto;">Default</span>`;
            else if (file.layer === 'common') layerBadge = `<span class="badge badge-info" style="font-size:0.65rem; margin-left:auto;">Common</span>`;
            else if (file.layer === 'secrets') layerBadge = `<span class="badge badge-warning" style="font-size:0.65rem; margin-left:auto;">Secrets</span>`;
            else if (file.layer === 'game') layerBadge = `<span class="badge badge-success" style="font-size:0.65rem; margin-left:auto;" title="${escapeHtml(t('config-game-badge-title'))}">${escapeHtml(t('config-game-badge'))}</span>`;
            else layerBadge = `<span class="badge badge-primary" style="font-size:0.65rem; margin-left:auto;">Instance</span>`;

            li.style.display = 'flex';
            li.style.justifyContent = 'space-between';
            li.style.alignItems = 'center';
            li.innerHTML = `<span>${escapeHtml(file.name)}</span>${layerBadge}`;
            li.addEventListener('click', () => loadConfigFile(file.path, file.name, li));
            el.configFilesList.appendChild(li);
            state.configFileEntries.push({ path: file.path, name: file.name, li: li });
            if (file.path === selectPath) loadConfigFile(file.path, file.name, li);
        });
    } catch (e) {
        if (seq !== state.configLoadSeq) return;
        el.configFilesList.innerHTML = `<li class="config-file-item text-danger">${t('config-load-error')}</li>`;
    }
}

async function loadConfigFile(path, name, liElement) {
    // Responses of files clicked before this one must not overwrite it.
    const seq = ++state.configLoadSeq;
    const serverId = state.activeConfigServer;

    // Highlight list item
    el.configFilesList.querySelectorAll('.config-file-item').forEach(li => li.classList.remove('active'));
    liElement.classList.add('active');

    el.editorActiveFile.textContent = name;
    el.editorTextarea.value = t('config-loading-file');
    el.editorTextarea.disabled = true;
    el.btnConfigSave.disabled = true;

    // Nothing of the previous file may remain until this one is loaded: no form, no save base.
    state.activeConfigFile = path;
    state.activeConfigContent = '';
    state.parsedConfigItems = [];
    state.formBaseContent = '';
    el.configFormBuilder.innerHTML = '';
    el.configModeToggle.style.display = 'none';
    setConfigView('raw');

    try {
        const res = await apiFetch(`/api/servers/${serverId}/configs/file?path=${encodeURIComponent(path)}`);
        const data = await res.json().catch(() => ({}));
        if (seq !== state.configLoadSeq) return;
        if (!res.ok || typeof data.content !== 'string') {
            el.editorTextarea.value = data.error ? `${t('config-load-file-error')} (${data.error})` : t('config-load-file-error');
            return;
        }

        state.activeConfigContent = data.content;
        el.editorTextarea.value = data.content;
        // Configs of a read-only or offline node can be read, not saved.
        const locked = serverWriteLocked(serverId);
        el.editorTextarea.disabled = false;
        el.editorTextarea.readOnly = locked;
        el.btnConfigSave.disabled = locked;
        el.btnConfigSave.title = locked ? serverWriteLockedText(serverId) : '';
        updateEditorLineNumbers();

        // Parse config and build form. LinuxGSM configs that set nothing yet (common.cfg and the
        // instance config by default) still get the form, which explains where values come from.
        state.parsedConfigItems = parseConfig(data.content, path);
        state.formBaseContent = data.content;
        if (state.parsedConfigItems.length > 0 || isLgsmConfigPath(path)) {
            el.configModeToggle.style.display = 'flex';
            buildConfigForm(state.parsedConfigItems);
            setConfigView('form');
        } else {
            // Nothing the form could show: text view only
            setConfigView('raw');
        }
    } catch (e) {
        if (seq === state.configLoadSeq) el.editorTextarea.value = t('config-load-file-error');
    }
}

async function saveConfigFile() {
    if (!state.activeConfigServer || !state.activeConfigFile) return;
    
    el.btnConfigSave.disabled = true;
    el.btnConfigSave.textContent = t('saving');
    
    const content = state.configMode === 'form' ? configFormContent() : el.editorTextarea.value;
    
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
            const result = await res.json().catch(() => ({}));
            if (result.redirected_to) {
                // LinuxGSM restores _default.cfg on every run; the backend stored the changes in the instance config.
                const target = result.redirected_to.split('/').pop();
                el.btnConfigSave.textContent = t('modal-config-btn-save');
                if (result.keys && result.keys.length > 0) {
                    alert(t('config-default-redirected').replace('{file}', target).replace('{keys}', result.keys.join(', ')));
                    await openConfigEditor(state.activeConfigServer, result.redirected_to);
                } else {
                    alert(t('config-default-no-changes'));
                    el.btnConfigSave.disabled = false;
                }
                return;
            }
            state.activeConfigContent = content;
            el.editorTextarea.value = content;
            updateEditorLineNumbers();
            // The saved text is the new base for detecting changed form fields.
            state.formBaseContent = content;
            state.parsedConfigItems = parseConfig(content, state.activeConfigFile);
            if (state.configMode === 'form') {
                const scroll = el.editorFormWrapper.scrollTop;
                buildConfigForm(state.parsedConfigItems);
                el.editorFormWrapper.scrollTop = scroll;
            }

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
            iconHtml = `<img class="game-card-icon" src="${escapeHtml(game.icon)}" alt="${escapeHtml(game.name)}" onerror="this.style.display='none'; this.nextElementSibling.style.display='flex';">`;
        }
        
        card.innerHTML = `
            ${iconHtml}
            <div class="game-icon-placeholder game-card-icon-placeholder" style="display: ${game.icon ? 'none' : 'flex'};">
                <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="12 2 2 7 12 12 22 7 12 2"></polygon><polyline points="2 17 12 22 22 17"></polyline><polyline points="2 12 12 17 22 12"></polyline></svg>
            </div>
            <div>
                <h4 title="${escapeHtml(game.name)}">${escapeHtml(game.name)}</h4>
                <span>${escapeHtml(game.cmd)}</span>
            </div>
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
    applyInstallerMode();
    populateInstallTargets();

    el.installFormContainer.classList.remove('hidden');
    
    // Smooth scroll down to form on mobile
    if (window.innerWidth <= 992) {
        el.installFormContainer.scrollIntoView({ behavior: 'smooth' });
    }
}

// Sets data-i18n on node and translates it, so the text survives a language switch.
function setI18n(node, key) {
    if (!node) return;
    node.setAttribute('data-i18n', key);
    node.textContent = t(key);
}

// Autostart and schedule tools: in Docker mode the container restart policy replaces systemd,
// and the dashboard runs the schedules itself (no crontab lines to copy).
function applyToolsMode() {
    const srv = state.servers.find(s => s.id === el.settingsServerSelect.value);
    const systemdCard = el.templateSystemd ? el.templateSystemd.closest('.tool-subcard') : null;
    if (systemdCard) systemdCard.classList.toggle('hidden', !capable('autostart', srv));
    if (!el.templateCron) return;
    const dashboardScheduler = !capable('linux_users', srv) && capable('cron', srv);
    el.templateCron.classList.toggle('hidden', dashboardScheduler);
    const card = el.templateCron.closest('.tool-subcard');
    if (!card) return;
    setI18n(card.querySelector('p'), dashboardScheduler ? 'settings-tools-cron-desc-docker' : 'settings-tools-cron-desc');
    setI18n(card.querySelector('#settings-tools-btn-cron span'), dashboardScheduler ? 'settings-tools-btn-cron-docker' : 'settings-tools-btn-cron');
}

// installTarget returns the node selected in the installer ("" for this machine) and its
// capabilities, so the form matches the runtime of the target.
function installTarget() {
    const select = document.getElementById('install-target');
    const nodeId = select ? select.value : '';
    const node = nodeId ? (state.installNodes || []).find(n => n.id === nodeId) : null;
    return { nodeId: node ? node.id : '', server: node ? { node: node.id, capabilities: node.hello && node.hello.capabilities } : null };
}

// populateInstallTargets offers this machine and every connected read-write node (administrators
// only; the installer is an administrator feature).
async function populateInstallTargets() {
    const group = document.getElementById('install-target-group');
    const select = document.getElementById('install-target');
    if (!group || !select) return;
    let nodes = [];
    if (state.currentUser && state.currentUser.role === 'admin') {
        try {
            const res = await apiFetch('/api/admin/nodes');
            if (res.ok) nodes = (await res.json()).nodes || [];
        } catch (e) {
            nodes = [];
        }
    }
    state.installNodes = nodes.filter(n => n.online && n.compatible && n.effective_mode === 'read-write');
    const previous = select.value;
    select.innerHTML = '';
    const here = document.createElement('option');
    here.value = '';
    here.textContent = t('inst-target-here');
    select.appendChild(here);
    state.installNodes.forEach(n => {
        const opt = document.createElement('option');
        opt.value = n.id;
        opt.textContent = `${n.name} (${n.id})`;
        select.appendChild(opt);
    });
    if (state.installNodes.some(n => n.id === previous)) select.value = previous;
    group.classList.toggle('hidden', nodes.length === 0);
    if (!select.dataset.bound) {
        select.addEventListener('change', applyInstallerMode);
        select.dataset.bound = '1';
    }
    applyInstallerMode();
}

// In Docker mode the installer creates a container: the name field names the server and its
// container, a Linux password and a custom directory do not exist there. With a node selected,
// the node's runtime decides.
function applyInstallerMode() {
    const docker = !capable('linux_users', installTarget().server);
    const label = document.querySelector('label[for="install-username"]');
    const help = label ? label.parentElement.querySelector('small') : null;
    setI18n(label, docker ? 'inst-name-label' : 'inst-user-label');
    setI18n(help, docker ? 'inst-name-help' : 'inst-user-help');
    ['install-password', 'install-dir'].forEach(id => {
        const input = document.getElementById(id);
        if (!input) return;
        const group = input.closest('.form-group');
        if (group) group.classList.toggle('hidden', docker);
        if (docker) input.value = '';
    });
}

function handleGameInstall(e) {
    e.preventDefault();

    const gameId = el.installGameId.value;
    const gamesList = state.games.length > 0 ? state.games : popularGames;
    const game = gamesList.find(g => g.id === gameId);
    if (!game) return;

    const username = el.installUsername.value;
    const password = el.installPassword.value;
    const installDirInput = document.getElementById('install-dir');
    const installDir = installDirInput ? installDirInput.value.trim() : '';

    runStreamModal({
        title: t('modal-stream-title-install').replace('{game}', game.name),
        runningText: t('status-installing-action'),
        url: '/api/games/install',
        body: { game: game.cmd, user: username, password, install_dir: installDir, lang: state.language, node: installTarget().nodeId },
        errorText: t('modal-stream-err-install')
    });
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
    
    // Canvas cannot resolve CSS variables, so read the theme colours explicitly.
    const theme = getComputedStyle(document.body);
    const cssVar = (name, fallback) => theme.getPropertyValue(name).trim() || fallback;
    const mutedColor = cssVar('--text-muted', '#8b929b');

    // Draw Grid Lines & Y Labels
    ctx.strokeStyle = cssVar('--chart-grid', 'rgba(255, 255, 255, 0.06)');
    ctx.lineWidth = 1;
    ctx.fillStyle = mutedColor;
    ctx.font = `11px ${cssVar('--font-mono', 'monospace')}`;
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
    const drawLine = (data, color) => {
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
        ctx.lineWidth = 1.5;
        ctx.stroke();
        
        // Light area fill
        ctx.lineTo(padding.left + (len - 1) * xStep, padding.top + chartHeight);
        ctx.lineTo(padding.left, padding.top + chartHeight);
        ctx.closePath();
        ctx.globalAlpha = 0.08;
        ctx.fillStyle = color;
        ctx.fill();
        ctx.globalAlpha = 1;
    };
    
    drawLine(cpuData, cssVar('--chart-cpu', '#3fa7a0'));
    drawLine(ramData, cssVar('--chart-ram', '#c9a24a'));
    
    // Draw X labels (Timestamps, show 5 labels max)
    if (len > 0) {
        ctx.fillStyle = mutedColor;
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
                <td colspan="8" class="text-center text-muted" style="padding:2rem;">${t('overview-no-active-processes')}</td>
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
        
        const disk = diskSummary(srv.disk);
        tr.innerHTML = `
            <td><strong>${escapeHtml(srv.name)}</strong></td>
            <td><code>${escapeHtml(srv.user)}</code></td>
            <td>${statusBadge}</td>
            <td>${srv.status === 'running' && srv.query_online ? `${escapeHtml(srv.num_players)} / ${escapeHtml(srv.max_players)}` : '-'}</td>
            <td>${srv.status === 'running' ? `${srv.cpu.toFixed(1)} %` : '-'}</td>
            <td>${srv.status === 'running' ? `${srv.ram.toFixed(2)} GB` : '-'}</td>
            <td title="${escapeHtml(disk.title)}">${escapeHtml(disk.text)}</td>
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
        if (data.csrf_token) state.csrfToken = data.csrf_token;
        applyUpdateStatus(data.update);
        
        el.settingsOs.textContent = data.os;
        el.settingsPid.textContent = data.pid;
        
        const versionVal = document.querySelector('[data-i18n="settings-info-version"]');
        if (versionVal && versionVal.nextElementSibling) {
            versionVal.nextElementSibling.textContent = data.version;
        }

        renderSettingsMode();
        el.settingsMode.className = 'info-value highlight text-success';
        
        const pathsCard = document.getElementById('settings-server-paths-card');
        if (state.currentUser && state.currentUser.role === 'admin') {
            if (capable('self_update')) checkDashboardUpdate(false);
            if (capable('server_paths')) {
                if (pathsCard) pathsCard.classList.remove('hidden');
                loadServerPaths();
            } else if (pathsCard) {
                pathsCard.classList.add('hidden');
            }
        } else if (pathsCard) {
            pathsCard.classList.add('hidden');
        }
        
        // Ensure active subtab is visible
        switchSettingsSubtab(state.settingsSubtab || 'general');

        // Populate tools dropdown (the page may have been opened directly on #settings)
        await ensureServersLoaded();
        populateSettingsServerSelect();

        applySettingsPermissions();
        loadTwoFactorStatus();

        loadScheduleTimeZone();

        // Check Firewall Status & Rights (administrators only)
        if (state.currentUser && state.currentUser.role === 'admin' && capable('firewall')) {
            loadFirewallStatus();
        }
    } catch (e) {
        console.error(e);
    }
}

// -------------------------------------------------------------
// Additional server directories (e.g. servers on a separate SSD)
// -------------------------------------------------------------

async function loadServerPaths() {
    const textarea = document.getElementById('server-paths-input');
    if (!textarea) return;
    try {
        const res = await apiFetch('/api/admin/server-paths');
        if (res.status !== 200) return;
        const data = await res.json();
        textarea.value = (data.paths || []).join('\n');
    } catch (e) {
        console.error('Failed to load server paths:', e);
    }
}

// Time zone of the dashboard's own scheduler (Docker mode). On a host the system crontab and
// its time zone apply, so the card is only shown when the runtime supports the setting.
const fallbackTimeZones = ['UTC', 'Europe/Berlin', 'Europe/Vienna', 'Europe/Zurich', 'Europe/London', 'Europe/Paris',
    'America/New_York', 'America/Chicago', 'America/Los_Angeles', 'Asia/Tokyo', 'Australia/Sydney'];

function renderScheduleTimeZone(info) {
    const select = document.getElementById('settings-tz-select');
    const now = document.getElementById('settings-tz-now-text');
    if (!select || !info) return;
    let zones = fallbackTimeZones;
    try {
        if (Intl.supportedValuesOf) zones = Intl.supportedValuesOf('timeZone');
    } catch (e) { /* older browsers */ }
    if (!zones.includes('UTC')) zones = ['UTC', ...zones];
    if (info.timezone && !zones.includes(info.timezone)) zones = [info.timezone, ...zones];
    select.innerHTML = '';
    const def = document.createElement('option');
    def.value = '';
    def.textContent = t('settings-tz-default').replace('{zone}', info.default || 'UTC');
    select.appendChild(def);
    zones.forEach(z => {
        const o = document.createElement('option');
        o.value = z;
        o.textContent = z;
        select.appendChild(o);
    });
    select.value = info.timezone || '';
    if (now) now.textContent = `${(info.now || '').slice(0, 16).replace('T', ' ')} (${info.effective})`;
}

async function loadScheduleTimeZone() {
    const card = document.getElementById('settings-timezone-card');
    if (!card) return;
    const isAdmin = !!(state.currentUser && state.currentUser.role === 'admin');
    const caps = state.lastSettingsData && state.lastSettingsData.capabilities;
    // Only when the backend reports it: capable() treats unknown features as available.
    card.classList.toggle('hidden', !isAdmin || !(caps && caps.schedule_timezone === true));
    if (card.classList.contains('hidden')) return;
    try {
        const res = await apiFetch('/api/admin/timezone');
        if (res.status === 200) renderScheduleTimeZone(await res.json());
    } catch (e) {
        console.error('Time zone error:', e);
    }
}

async function saveScheduleTimeZone() {
    const select = document.getElementById('settings-tz-select');
    const message = document.getElementById('settings-tz-message');
    if (!select || !message) return;
    message.className = 'info-message';
    message.textContent = t('saving');
    try {
        const res = await apiFetch('/api/admin/timezone', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ timezone: select.value })
        });
        const data = await res.json();
        if (res.status === 200) {
            renderScheduleTimeZone(data);
            message.className = 'info-message text-success';
            message.textContent = t('settings-tz-saved').replace('{zone}', data.effective);
        } else {
            message.className = 'info-message text-danger';
            message.textContent = data.error || t('status-error');
        }
    } catch (e) {
        message.className = 'info-message text-danger';
        message.textContent = t('connection-error');
    }
}

async function saveServerPaths() {
    const textarea = document.getElementById('server-paths-input');
    const message = document.getElementById('server-paths-message');
    if (!textarea || !message) return;
    const paths = textarea.value.split('\n').map(p => p.trim()).filter(p => p !== '');
    message.className = 'info-message';
    message.textContent = t('saving');
    try {
        const res = await apiFetch('/api/admin/server-paths', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ paths })
        });
        const data = await res.json();
        if (res.status === 200) {
            textarea.value = (data.paths || []).join('\n');
            message.className = 'info-message text-success';
            message.textContent = t('settings-paths-saved');
            state.servers = [];
            ensureServersLoaded();
        } else {
            message.className = 'info-message text-danger';
            message.textContent = data.error || t('status-error');
        }
    } catch (e) {
        message.className = 'info-message text-danger';
        message.textContent = t('connection-error');
    }
}

// Optional features of the server runtime (host or Docker), reported by /api/system/info.
// Unknown features count as available, which matches the host runtime.
// capable reports whether a runtime feature exists. With a server of a node, the node's runtime
// decides (a Docker node under a host dashboard has no Linux users, for example).
function capable(feature, server) {
    if (server && server.node) {
        return !server.capabilities || server.capabilities[feature] !== false;
    }
    const caps = state.lastSettingsData && state.lastSettingsData.capabilities;
    return !caps || caps[feature] !== false;
}

// Settings sub tabs and the permission they need. "general" (own password, system info) is always
// available; everything else mirrors the server-side checks.
function allowedSettingsSubtabs() {
    const isAdmin = !!(state.currentUser && state.currentUser.role === 'admin');
    return {
        general: true,
        server: isAdmin,
        cron: hasPerm('schedule') && capable('cron'),
        alerts: hasPerm('alerts'),
        security: isAdmin && capable('firewall'),
        nodes: isAdmin,
    };
}

function applySettingsPermissions() {
    const allowed = allowedSettingsSubtabs();
    Object.keys(allowed).forEach(tab => {
        const btn = document.getElementById(`settings-tab-btn-${tab}`);
        if (btn) btn.classList.toggle('hidden', !allowed[tab]);
    });
    // The tab bar is pointless when only "general" is left.
    const nav = document.querySelector('.settings-subtabs-nav');
    if (nav) nav.classList.toggle('hidden', Object.values(allowed).filter(Boolean).length <= 1);

    const isAdmin = !!(state.currentUser && state.currentUser.role === 'admin');
    const updateCard = document.getElementById('settings-update-card');
    if (updateCard) updateCard.classList.toggle('hidden', !isAdmin || !capable('self_update'));

    if (state.settingsSubtab && !allowed[state.settingsSubtab]) {
        switchSettingsSubtab('general');
    }
}

function switchSettingsSubtab(subtabName) {
    if (!allowedSettingsSubtabs()[subtabName]) subtabName = 'general';
    state.settingsSubtab = subtabName;
    const subtabs = ['general', 'server', 'cron', 'alerts', 'security', 'nodes'];
    subtabs.forEach(tab => {
        const btn = document.getElementById(`settings-tab-btn-${tab}`);
        const panel = document.getElementById(`settings-subtab-panel-${tab}`);
        if (btn) btn.classList.toggle('active', tab === subtabName);
        if (panel) panel.classList.toggle('hidden', tab !== subtabName);
    });

    if (el.settingsServerSelectBar) {
        const isServerTab = ['server', 'cron', 'alerts'].includes(subtabName);
        el.settingsServerSelectBar.classList.toggle('hidden', !isServerTab);
    }

    // The node list refreshes itself while it is visible.
    clearInterval(state.nodesInterval);
    state.nodesInterval = null;
    if (subtabName === 'nodes') {
        loadNodes();
        state.nodesInterval = setInterval(() => {
            if (state.lastActiveView === 'settings' && state.settingsSubtab === 'nodes') loadNodes();
        }, 5000);
    }
}

// -------------------------------------------------------------
// Nodes (game servers on other machines)
// -------------------------------------------------------------

function initNodesPanel() {
    const form = document.getElementById('nodes-add-form');
    if (!form) return;
    form.addEventListener('submit', async (ev) => {
        ev.preventDefault();
        const name = document.getElementById('nodes-add-name').value.trim();
        const mode = document.getElementById('nodes-add-mode').value;
        if (!name) return;
        if (await createNodePairing({ name, mode })) form.reset();
    });
    document.getElementById('nodes-refresh').addEventListener('click', loadNodes);
    document.getElementById('nodes-upgrade-all').addEventListener('click', upgradeAllNodes);
    const copy = (btnId, codeId) => {
        document.getElementById(btnId).addEventListener('click', async () => {
            const text = document.getElementById(codeId).textContent;
            try {
                await navigator.clipboard.writeText(text);
                const btn = document.getElementById(btnId);
                btn.textContent = t('nodes-copied');
                setTimeout(() => { btn.textContent = t('nodes-copy'); }, 1500);
            } catch (e) {
                // Clipboard access needs HTTPS; the command is selectable as fallback.
                window.getSelection().selectAllChildren(document.getElementById(codeId));
            }
        });
    };
    copy('nodes-command-copy', 'nodes-command');
    copy('nodes-command-direct-copy', 'nodes-command-direct');
}

// createNodePairing asks for a pairing command (new node or re-pairing) and shows it.
async function createNodePairing(payload) {
    try {
        const res = await apiFetch('/api/admin/nodes', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload),
        });
        const data = await res.json();
        if (!res.ok) {
            alert(`${t('nodes-error')}: ${data.error || res.status}`);
            return false;
        }
        const ticket = data.ticket || {};
        const minutes = Math.max(1, Math.round((new Date(ticket.expires) - Date.now()) / 60000));
        document.getElementById('nodes-command-intro').textContent = t('nodes-command-intro')
            .replace('{name}', ticket.name || ticket.node_id || '')
            .replace('{minutes}', minutes);
        document.getElementById('nodes-command').textContent = data.command || '';
        const directRow = document.getElementById('nodes-command-direct-row');
        directRow.classList.toggle('hidden', !data.command_direct);
        document.getElementById('nodes-command-direct').textContent = data.command_direct || '';
        document.getElementById('nodes-command-hint').textContent = t('nodes-command-hint');
        const box = document.getElementById('nodes-command-box');
        box.classList.remove('hidden');
        box.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
        loadNodes();
        return true;
    } catch (e) {
        alert(t('connection-error'));
        return false;
    }
}

function nodeStatusBadge(node) {
    if (node.online && node.compatible) return `<span class="badge badge-success">${t('nodes-status-online')}</span>`;
    if (node.online) return `<span class="badge badge-warning">${t('nodes-status-update')}</span>`;
    if (node.problem) return `<span class="badge badge-danger">${t('nodes-status-problem')}</span>`;
    return `<span class="badge badge-secondary">${t('nodes-status-offline')}</span>`;
}

// nodeLoad shows CPU, RAM and free disk space of a connected node.
function nodeLoad(node) {
    const h = node.host;
    if (!h) return '-';
    const num = v => Number(v) || 0;
    const ram = `RAM ${num(h.ramUsed).toFixed(1)} / ${num(h.ramTotal).toFixed(1)} GB`;
    const disk = `${t('overview-free')}: ${num(h.diskFree).toFixed(0)} / ${num(h.diskTotal).toFixed(0)} GB`;
    return `CPU ${num(h.cpu).toFixed(0)} %<div class="node-sub">${escapeHtml(ram)}</div><div class="node-sub">${escapeHtml(disk)}</div>`;
}

function formatNodeTime(value) {
    if (!value || String(value).startsWith('0001-')) return '-';
    const d = new Date(value);
    return isNaN(d) ? '-' : d.toLocaleString(state.language === 'de' ? 'de-DE' : 'en-GB');
}

async function loadNodes() {
    const list = document.getElementById('nodes-list');
    if (!list) return;
    let data;
    try {
        const res = await apiFetch('/api/admin/nodes');
        if (!res.ok) return;
        data = await res.json();
    } catch (e) {
        return;
    }
    state.nodes = data.nodes || [];
    const upgradeAll = document.getElementById('nodes-upgrade-all');
    if (upgradeAll) upgradeAll.classList.toggle('hidden', !state.nodes.some(n => n.update && n.update.possible));
    // Do not rebuild the table while a mode select is open.
    if (list.contains(document.activeElement) && document.activeElement.tagName === 'SELECT') return;
    if (state.nodes.length === 0) {
        list.innerHTML = `<p class="text-muted mb-0">${t('nodes-empty')}</p>`;
        return;
    }
    const modeOption = (value, current) =>
        `<option value="${value}" ${value === current ? 'selected' : ''}>${t('nodes-mode-' + value)}</option>`;
    const rows = state.nodes.map(node => {
        const id = jsArg(node.id);
        const hello = node.hello || {};
        const effective = node.effective_mode && node.effective_mode !== node.mode
            ? `<div class="node-sub">${escapeHtml(t('nodes-effective').replace('{mode}', t('nodes-mode-' + node.effective_mode)))}</div>` : '';
        const problem = node.problem ? `<div class="node-problem">${escapeHtml(node.problem)}</div>` : '';
        return `
            <tr>
                <td><strong>${escapeHtml(node.name)}</strong><div class="node-sub mono">${escapeHtml(node.id)}</div></td>
                <td>${nodeStatusBadge(node)}${problem}</td>
                <td>
                    <select class="form-control form-control-sm" onchange="updateNodeMode(${id}, this.value)" aria-label="${t('nodes-col-mode')}">
                        ${modeOption('read-only', node.mode)}${modeOption('read-write', node.mode)}
                    </select>
                    ${effective}
                </td>
                <td>${escapeHtml(node.version || '-')}<div class="node-sub">${escapeHtml(hello.capabilities ? hello.capabilities.runtime : '')}</div>${nodeUpdateCell(node)}</td>
                <td>${escapeHtml(node.hostname || '-')}<div class="node-sub">${escapeHtml(formatNodeTime(node.last_seen))}</div></td>
                <td class="node-load">${nodeLoad(node)}</td>
                <td>
                    <div class="node-actions">
                        <button type="button" class="btn btn-secondary btn-sm" onclick="renameNode(${id})">${t('nodes-rename')}</button>
                        <button type="button" class="btn btn-secondary btn-sm" onclick="repairNode(${id})">${t('nodes-repair')}</button>
                        <button type="button" class="btn btn-danger btn-sm" onclick="removeNode(${id})">${t('nodes-remove')}</button>
                    </div>
                </td>
            </tr>`;
    }).join('');
    list.innerHTML = `
        <div class="nodes-list-scroll">
            <table>
                <thead><tr>
                    <th>${t('nodes-col-node')}</th><th>${t('nodes-col-status')}</th><th>${t('nodes-col-mode')}</th>
                    <th>${t('nodes-col-version')}</th><th>${t('nodes-col-host')}</th><th>${t('nodes-col-load')}</th><th></th>
                </tr></thead>
                <tbody>${rows}</tbody>
            </table>
        </div>`;
}

function findNode(nodeId) {
    return (state.nodes || []).find(n => n.id === nodeId);
}

async function updateNode(nodeId, name, mode) {
    try {
        const res = await apiFetch('/api/admin/nodes/update', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: nodeId, name, mode }),
        });
        if (!res.ok) {
            const data = await res.json().catch(() => ({}));
            alert(`${t('nodes-error')}: ${data.error || res.status}`);
        }
    } catch (e) {
        alert(t('connection-error'));
    }
    loadNodes();
}

function updateNodeMode(nodeId, mode) {
    const node = findNode(nodeId);
    if (node) updateNode(nodeId, node.name, mode);
}

function renameNode(nodeId) {
    const node = findNode(nodeId);
    if (!node) return;
    const name = prompt(t('nodes-rename-prompt'), node.name);
    if (name && name.trim() && name.trim() !== node.name) updateNode(nodeId, name.trim(), node.mode);
}

function repairNode(nodeId) {
    const node = findNode(nodeId);
    if (!node) return;
    if (!confirm(t('nodes-repair-confirm').replace('{name}', node.name))) return;
    createNodePairing({ repair_id: nodeId, mode: node.mode });
}

// nodeUpdateCell shows whether a node runs the dashboard's version and offers the update.
function nodeUpdateCell(node) {
    const u = node.update || {};
    if (u.possible) {
        return `<button type="button" class="btn btn-primary btn-xs mt-1" onclick="upgradeNode(${jsArg(node.id)})">${escapeHtml(t('nodes-upgrade-btn').replace('{version}', u.target))}</button>`;
    }
    if (!u.reason || u.reason === 'offline' || u.reason === 'current' || u.reason === 'dashboard-dev') return '';
    return `<div class="node-sub">${escapeHtml(t('nodes-upgrade-' + u.reason))}</div>`;
}

// upgradeNode asks the node to install the dashboard's release; the node verifies the release
// itself and restarts, so it is briefly offline.
async function upgradeNode(nodeId, quiet) {
    const node = findNode(nodeId);
    if (!node) return false;
    if (!quiet && !confirm(t('nodes-upgrade-confirm').replace('{name}', node.name).replace('{version}', (node.update || {}).target || ''))) return false;
    try {
        const res = await apiFetch('/api/admin/nodes/upgrade', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: nodeId }),
        });
        if (!res.ok) {
            const data = await res.json().catch(() => ({}));
            alert(`${node.name}: ${data.error || res.status}`);
            return false;
        }
        return true;
    } catch (e) {
        alert(t('connection-error'));
        return false;
    } finally {
        setTimeout(loadNodes, 3000);
    }
}

async function upgradeAllNodes() {
    const nodes = (state.nodes || []).filter(n => n.update && n.update.possible);
    if (nodes.length === 0) return;
    const target = nodes[0].update.target;
    if (!confirm(t('nodes-upgrade-all-confirm').replace('{count}', nodes.length).replace('{version}', target))) return;
    const btn = document.getElementById('nodes-upgrade-all');
    if (btn) btn.disabled = true;
    // One after the other, so a broken release does not take every node down at once.
    for (const n of nodes) {
        if (!(await upgradeNode(n.id, true))) break;
    }
    if (btn) btn.disabled = false;
    loadNodes();
}

async function removeNode(nodeId) {
    const node = findNode(nodeId);
    if (!node || !confirm(t('nodes-remove-confirm').replace('{name}', node.name))) return;
    try {
        const res = await apiFetch('/api/admin/nodes/delete', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: nodeId }),
        });
        if (!res.ok) {
            const data = await res.json().catch(() => ({}));
            alert(`${t('nodes-error')}: ${data.error || res.status}`);
        }
    } catch (e) {
        alert(t('connection-error'));
    }
    loadNodes();
}

async function loadFirewallStatus() {
    if (!el.firewallStatusBadge) return;
    try {
        const res = await apiFetch('/api/system/firewall');
        if (res.status === 200) {
            const data = await res.json();
            if (!data.hasPermission) {
                el.firewallStatusBadge.className = 'badge badge-warning';
                el.firewallStatusBadge.textContent = t('firewall-status-restricted');
            } else if (data.active) {
                el.firewallStatusBadge.className = 'badge badge-success';
                el.firewallStatusBadge.textContent = `${t('firewall-status-active')} (${data.tool.toUpperCase()})`;
            } else {
                el.firewallStatusBadge.className = 'badge badge-secondary';
                el.firewallStatusBadge.textContent = `${t('firewall-status-inactive')} (${data.tool})`;
            }

            el.firewallMessageText.textContent = data.message || '';

            if (data.rules && data.rules.length > 0) {
                el.firewallRulesBox.classList.remove('hidden');
                el.firewallRulesBox.innerHTML = `<strong>${t('firewall-rules-header')}:</strong><br>` + data.rules.map(r => escapeHtml(r)).join('<br>');
            } else {
                el.firewallRulesBox.classList.add('hidden');
            }
        }
    } catch (e) {
        console.error('Firewall status error:', e);
        if (el.firewallStatusBadge) {
            el.firewallStatusBadge.className = 'badge badge-secondary';
            el.firewallStatusBadge.textContent = t('firewall-status-unknown');
        }
    }
}

function renderSettingsMode() {
    const caps = state.lastSettingsData && state.lastSettingsData.capabilities;
    el.settingsMode.textContent = caps && caps.runtime === 'docker' ? t('settings-mode-docker') : t('settings-mode-prod');
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
    if (!el.settingsServerSelect) return;
    const current = el.settingsServerSelect.value;
    el.settingsServerSelect.innerHTML = `<option value="">${t('settings-tools-select-default')}</option>`;
    
    state.servers.forEach(server => {
        const opt = document.createElement('option');
        opt.value = server.id;
        opt.textContent = serverLabel(server);
        el.settingsServerSelect.appendChild(opt);
    });
    
    if (current && state.servers.find(s => s.id === current)) {
        el.settingsServerSelect.value = current;
    }
    handleSettingsServerChange();
}

// applySettingsWriteLock disables the server tools, schedule and alert controls while the selected
// server runs on a read-only or offline node (the node would refuse the changes). Tags stay
// editable: the dashboard keeps them itself. Controls added later by the loaders are caught by
// the observer installed in watchSettingsWriteLock.
function applySettingsWriteLock(allowLock = true) {
    const serverId = el.settingsServerSelect ? el.settingsServerSelect.value : '';
    const locked = allowLock && !!serverId && serverWriteLocked(serverId);
    [el.settingsToolsArea, el.settingsCronArea, el.alertsSettingsPanel].forEach(container => {
        if (!container) return;
        container.querySelectorAll('input, select, textarea, button').forEach(c => {
            if (c.dataset.keepEnabled) return;
            if (locked) {
                if (!c.disabled) {
                    c.disabled = true;
                    c.dataset.nodeLocked = '1';
                }
            } else if (c.dataset.nodeLocked) {
                c.disabled = false;
                delete c.dataset.nodeLocked;
            }
        });
    });
    const note = document.getElementById('settings-node-lock-note');
    if (note) {
        note.textContent = locked ? serverWriteLockedText(serverId) : '';
        note.classList.toggle('hidden', !locked);
    }
}

function watchSettingsWriteLock() {
    const observer = new MutationObserver(() => applySettingsWriteLock());
    [el.settingsToolsArea, el.settingsCronArea, el.alertsSettingsPanel].forEach(container => {
        if (container) observer.observe(container, { childList: true, subtree: true });
    });
}

function handleSettingsServerChange() {
    if (!el.settingsServerSelect) return;
    // First give back what an earlier lock disabled, so the permission checks below decide alone;
    // the lock for the new server follows once the controls are set up.
    applySettingsWriteLock(false);
    setTimeout(applySettingsWriteLock, 0);
    const serverID = el.settingsServerSelect.value;
    const hasServer = !!serverID;
    
    if (el.settingsToolsArea) el.settingsToolsArea.classList.toggle('hidden', !hasServer);
    if (el.settingsCronArea) el.settingsCronArea.classList.toggle('hidden', !hasServer);
    if (el.alertsSettingsPanel) el.alertsSettingsPanel.classList.toggle('hidden', !hasServer);

    if (el.settingsServerPromptServer) el.settingsServerPromptServer.classList.toggle('hidden', hasServer);
    if (el.settingsServerPromptCron) el.settingsServerPromptCron.classList.toggle('hidden', hasServer);
    if (el.settingsServerPromptAlerts) el.settingsServerPromptAlerts.classList.toggle('hidden', hasServer);

    if (!hasServer) {
        resetSettingsAlerts();
        return;
    }
    
    const server = state.servers.find(s => s.id === serverID);
    if (!server) return;
    
    if (el.settingsServerTagInput) {
        el.settingsServerTagInput.value = server.tag || '';
    }

    loadCronSchedule(serverID);
    loadServerStopMode(serverID);
    
    // 1. Generate Systemd template (same as backend/autostart.go)
    const serverDir = server.dir || `/home/${server.user}`;
    const systemdCode = `# /etc/systemd/system/linuxgsm-${server.user}-${server.script}.service
[Unit]
Description=LinuxGSM ${server.name} Server
After=network-online.target
Wants=network-online.target

[Service]
Type=forking
User=${server.user}
WorkingDirectory=${serverDir}
RemainAfterExit=yes
ExecStart=${serverDir}/${server.script} start
ExecStop=-${serverDir}/${server.script} stop
Restart=no
TimeoutStartSec=15min
TimeoutStopSec=5min

[Install]
WantedBy=multi-user.target`;

    // 2. Generate Cronjob template
    const isDe = state.language === 'de';
    const cronCode = `# LinuxGSM ${server.script}: scheduled maintenance
# 1. ${isDe ? 'Server auf Abstürze überwachen (alle 5 Minuten)' : 'Monitor server for crashes (every 5 minutes)'}
*/5 * * * * ${serverDir}/${server.script} monitor > /dev/null 2>&1

# 2. ${isDe ? 'Automatische Updateprüfung (alle 30 Minuten, startet bei Update neu)' : 'Automatic update check (every 30 minutes, restarts on update)'}
*/30 * * * * ${serverDir}/${server.script} update > /dev/null 2>&1

# 3. ${isDe ? 'Wöchentliches Update von LinuxGSM selbst (jeden Sonntag um 00:00 Uhr)' : 'Weekly update of LinuxGSM itself (every Sunday at 00:00 AM)'}
0 0 * * 0 ${serverDir}/${server.script} update-lgsm > /dev/null 2>&1`;

    if (el.templateSystemd) el.templateSystemd.textContent = systemdCode;
    if (el.templateCron) el.templateCron.textContent = cronCode;
    applyToolsMode();
    
    // 3. Configure alerts and load settings
    const isAdmin = state.currentUser && state.currentUser.role === 'admin';
    if (el.settingsBtnOpenClone) {
        el.settingsBtnOpenClone.classList.toggle('hidden', !isAdmin);
    }

    if (hasPerm('alerts')) {
        setAlertFieldsDisabled(false);
        loadAlertSettings();
    } else {
        resetSettingsAlerts();
    }
}

// Permission check mirroring the backend RBAC (admins hold every permission).
function hasPerm(permission) {
    const u = state.currentUser;
    if (!u) return false;
    if (u.role === 'admin') return true;
    return Array.isArray(u.permissions) && u.permissions.includes(permission);
}

function setAlertFieldsDisabled(disabled) {
    if (el.alertsFields) el.alertsFields.querySelectorAll('input, select').forEach(i => { i.disabled = disabled; });
    if (el.alertsSettingsSaveBtn) el.alertsSettingsSaveBtn.disabled = disabled;
}


// -------------------------------------------------------------
// Config Editor Mode & Parsing Helpers
// -------------------------------------------------------------

// setConfigView shows the form or the text view without converting between them.
function setConfigView(mode) {
    state.configMode = mode;
    el.btnConfigForm.classList.toggle('active', mode === 'form');
    el.btnConfigRaw.classList.toggle('active', mode !== 'form');
    el.editorFormWrapper.classList.toggle('hidden', mode !== 'form');
    el.editorTextWrapper.classList.toggle('hidden', mode === 'form');
}

function switchConfigMode(mode) {
    if (!state.activeConfigFile || mode === state.configMode) return;
    if (mode === 'form') {
        // Rebuild the form from the text, which may have been edited. The form always matches the
        // text, even when no values are left: a stale form would save the old text on top.
        const currentText = el.editorTextarea.value;
        state.parsedConfigItems = parseConfig(currentText, state.activeConfigFile);
        state.formBaseContent = currentText;
        buildConfigForm(state.parsedConfigItems);
    } else {
        // Serialize form values back to text in case they modified them in the form
        el.editorTextarea.value = configFormContent();
        updateEditorLineNumbers();
    }
    setConfigView(mode);
}

// configFormContent applies the changed form fields to the text the form was built from. Fields
// the user did not touch are never rewritten.
function configFormContent() {
    const base = state.formBaseContent;
    const changes = new Map();
    state.parsedConfigItems.forEach(item => {
        const inputEl = document.getElementById(`config-form-val-${item.id}`);
        if (!inputEl) return;
        if (item.type === 'boolean') {
            if (inputEl.checked !== item.checked) changes.set(item.id, formatConfigBool(item.value, inputEl.checked));
        } else if (inputEl.value !== item.value) {
            changes.set(item.id, inputEl.value);
        }
    });
    if (changes.size === 0) return base;
    const ext = state.activeConfigFile.split('.').pop().toLowerCase();
    return ext === 'json' ? serializeJsonForm(base, state.parsedConfigItems, changes)
                          : serializeConfigForm(base, state.parsedConfigItems, changes, ext === 'cfg' && isLgsmConfigPath(state.activeConfigFile));
}

// Keeps the spelling of the original value (true/True/TRUE).
function formatConfigBool(original, checked) {
    const word = checked ? 'true' : 'false';
    if (original === original.toUpperCase()) return word.toUpperCase();
    if (original[0] === original[0].toUpperCase()) return word[0].toUpperCase() + word.slice(1);
    return word;
}

// JSON numbers beyond 2^53 lose precision in JSON.parse, so such files are only edited as text.
const UNSAFE_JSON_NUMBER = /[:\[,]\s*-?\d{16,}/;

// parseConfig returns the editable values of a config. Every item remembers its line (or JSON
// key), so duplicate keys and keys in different INI sections stay separate.
function parseConfig(content, path) {
    const ext = path.split('.').pop().toLowerCase();
    const items = [];
    const typed = (value) => {
        if (/^(true|false)$/i.test(value)) return 'boolean';
        if (/^-?\d+(\.\d+)?$/.test(value)) return 'number';
        return 'text';
    };

    if (ext === 'json') {
        if (UNSAFE_JSON_NUMBER.test(content)) return [];
        try {
            const obj = JSON.parse(content);
            if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return [];
            Object.keys(obj).forEach(key => {
                const v = obj[key];
                if (v === null || typeof v === 'object') return;
                const value = String(v);
                items.push({
                    id: items.length,
                    key: key,
                    label: key,
                    value: value,
                    checked: v === true,
                    jsonType: typeof v,
                    type: typeof v === 'boolean' ? 'boolean' : (typeof v === 'number' ? 'number' : 'text'),
                    comments: ''
                });
            });
            return items;
        } catch (e) {
            return [];
        }
    }

    // LinuxGSM configs are bash; other .cfg files are game configs (Source: `hostname "x"`,
    // Arma: `hostname = "x";`).
    const bash = ext === 'cfg' && isLgsmConfigPath(path);
    const gameCfg = ext === 'cfg' && !bash;
    const lines = content.split('\n');
    const kvRegex = /^(\s*([\w\.\-]+)\s*[=:]\s*)(.*)$/;
    const cvarRegex = /^(\s*([A-Za-z_][\w\.\-]*)\s+)(\S.*)$/;
    let accumulatedComments = [];
    let section = '';

    lines.forEach((rawLine, index) => {
        const line = rawLine.replace(/\r$/, '');
        const trimmed = line.trim();
        if (trimmed.startsWith('#') || trimmed.startsWith('//') || trimmed.startsWith(';')) {
            const cleanComment = trimmed.replace(/^[#\/;*\s]+/, '');
            if (cleanComment.length > 0) {
                accumulatedComments.push(cleanComment);
            }
            return;
        }

        if (trimmed === '') {
            accumulatedComments = [];
            return;
        }

        const sectionMatch = trimmed.match(/^\[(.+)\]$/);
        if (sectionMatch) {
            section = sectionMatch[1];
            accumulatedComments = [];
            return;
        }

        let match = line.match(kvRegex);
        const cvar = !match && gameCfg;
        if (cvar) match = line.match(cvarRegex);
        if (!match) return;
        const rest = match[3];
        let value = rest.trim();
        let quote = '';
        let suffix = '';
        if (value[0] === '"' || value[0] === "'") {
            quote = value[0];
            const close = findClosingQuote(value, quote, bash);
            if (close < 0) return; // value spans several lines: raw view only
            suffix = value.slice(close + 1);
            value = value.slice(1, close);
            if (quote === '"' && bash) value = value.replace(/\\(["\\`$])/g, '$1');
        } else if (gameCfg) {
            // An unquoted game config value ends at a // comment or the ; of Arma configs.
            const word = value.match(/^([^\s;]+)(\s*;?\s*(\/\/.*)?)$/);
            if (!word) {
                if (cvar) return; // `class Missions {` and other commands are no values
            } else {
                value = word[1];
                suffix = word[2];
            }
        }

        const type = typed(value);
        items.push({
            id: items.length,
            key: match[2],
            label: section ? `[${section}] ${match[2]}` : match[2],
            value: value,
            checked: /^true$/i.test(value),
            type: type,
            comments: accumulatedComments.join(' '),
            line: index,
            prefix: match[1],
            quote: quote,
            suffix: suffix,
            cr: rawLine.endsWith('\r') ? '\r' : ''
        });

        accumulatedComments = [];
    });

    return items;
}

// isLgsmConfigPath reports whether path is one of the LinuxGSM configs in config-lgsm/<game>.
function isLgsmConfigPath(path) {
    return /\/config-lgsm\/[^/]+\/[^/]+\.cfg$/.test(path || '');
}

// Index of the quote that closes value[0], or -1. In bash configs \" does not close the string.
function findClosingQuote(value, quote, bashEscapes) {
    for (let i = 1; i < value.length; i++) {
        if (bashEscapes && quote === '"' && value[i] === '\\') {
            i++;
            continue;
        }
        if (value[i] === quote) return i;
    }
    return -1;
}

function buildConfigForm(items) {
    el.configFormBuilder.innerHTML = '';
    // Values of read-only or offline nodes can be looked at, not changed.
    const locked = serverWriteLocked(state.activeConfigServer);

    if (items.length === 0) {
        const path = state.activeConfigFile || '';
        const lgsm = isLgsmConfigPath(path);
        const hint = document.createElement('div');
        hint.className = 'text-muted';
        hint.textContent = t(lgsm ? 'config-form-empty-lgsm' : 'config-form-empty');
        el.configFormBuilder.appendChild(hint);
        const dir = path.slice(0, path.lastIndexOf('/') + 1);
        const def = lgsm && !path.endsWith('/_default.cfg') && state.configFileEntries.find(f => f.path === dir + '_default.cfg');
        if (def) {
            const btn = document.createElement('button');
            btn.className = 'btn btn-secondary btn-sm';
            btn.style.alignSelf = 'flex-start';
            btn.textContent = t('config-form-open-default');
            btn.addEventListener('click', () => loadConfigFile(def.path, def.name, def.li));
            el.configFormBuilder.appendChild(btn);
        }
        return;
    }

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
        keyEl.textContent = item.label;
        left.appendChild(keyEl);

        if (item.comments) {
            const descEl = document.createElement('div');
            descEl.className = 'config-form-description';
            descEl.textContent = item.comments;
            left.appendChild(descEl);
        }

        if (['gslt', 'apikey', 'steamaccount', 'sv_setsteamaccount'].includes(item.key.toLowerCase())) {
            const gsltBtn = document.createElement('a');
            gsltBtn.href = 'https://steamcommunity.com/dev/managegameservers';
            gsltBtn.target = '_blank';
            gsltBtn.className = 'btn btn-secondary btn-xs mt-1';
            gsltBtn.textContent = 'Valve GSLT Portal';
            gsltBtn.style.display = 'inline-block';
            gsltBtn.style.marginTop = '0.3rem';
            left.appendChild(gsltBtn);
        }

        formItem.appendChild(left);

        const container = document.createElement('div');
        container.className = 'config-form-input-container';

        if (item.type === 'boolean') {
            const switchEl = document.createElement('label');
            switchEl.className = 'switch';

            const check = document.createElement('input');
            check.type = 'checkbox';
            check.id = `config-form-val-${item.id}`;
            check.checked = item.checked;
            check.disabled = locked;
            switchEl.appendChild(check);

            const slider = document.createElement('span');
            slider.className = 'slider';
            switchEl.appendChild(slider);

            container.appendChild(switchEl);
        } else {
            // Numbers stay text: a number input would round values like 64-bit Steam IDs.
            const input = document.createElement('input');
            input.type = 'text';
            input.id = `config-form-val-${item.id}`;
            input.className = 'form-control form-control-sm';
            if (item.type === 'number') {
                input.inputMode = 'decimal';
                input.style.width = '180px';
            } else {
                input.style.minWidth = '280px';
            }
            input.value = item.value;
            input.readOnly = locked;
            container.appendChild(input);
        }

        formItem.appendChild(container);
        el.configFormBuilder.appendChild(formItem);
    });
}

function serializeConfigForm(originalContent, items, changes, bash) {
    const lines = originalContent.split('\n');
    items.forEach(item => {
        if (!changes.has(item.id)) return;
        let value = changes.get(item.id);
        let quote = item.quote;
        if (bash) {
            // LinuxGSM configs are bash. An edited value is always quoted (servername=My Server would
            // run "Server"). $ is taken literally (passwords) unless it starts a ${variable} reference.
            if (quote === '' || (quote === "'" && value.includes("'"))) quote = '"';
            if (quote === '"') {
                value = value.replace(/(["\\`])/g, '\\$1').replace(/\$(?!\{[A-Za-z_][A-Za-z0-9_]*\})/g, '\\$');
            }
        }
        lines[item.line] = `${item.prefix}${quote}${value}${quote}${item.suffix}${item.cr}`;
    });
    return lines.join('\n');
}

function serializeJsonForm(originalContent, items, changes) {
    try {
        const obj = JSON.parse(originalContent);
        items.forEach(item => {
            if (!changes.has(item.id)) return;
            const value = changes.get(item.id);
            if (item.jsonType === 'boolean') {
                obj[item.key] = /^true$/i.test(value);
            } else if (item.jsonType === 'number' && value.trim() !== '' && !isNaN(Number(value))) {
                obj[item.key] = Number(value);
            } else {
                obj[item.key] = value;
            }
        });
        const indent = (originalContent.match(/\n([ \t]+)"/) || [null, '  '])[1];
        return JSON.stringify(obj, null, indent) + (originalContent.endsWith('\n') ? '\n' : '');
    } catch (e) {
        return originalContent;
    }
}

// -------------------------------------------------------------
// Theme & Language (i18n) Switchers
// -------------------------------------------------------------

const i18n = {
    en: {
        "card-players": "Players",
        "card-map": "Map",
        "card-disk": "Disk",
        "card-more": "More",
        "card-select": "Select server",
        "card-edit-tag": "Edit tag",
        "card-measuring": "measuring",
        "card-no-query": "The server does not answer queries (not supported or port blocked)",
        "card-disk-backups": "Backups",
        "card-disk-free": "Free on disk",
        "card-disk-shared": "Directory shared with other instances",
        "card-disk-pending": "Not measured yet",
        "card-disk-scanning": "Measurement in progress",
        "card-disk-updated": "Measured at",
        "card-disk-error": "Incomplete",
        "overview-col-players": "Players",
        "overview-col-disk": "Disk",
        "menu-dashboard": "Dashboard",
        "menu-installer": "Game Installer",
        "menu-overview": "Overview",
        "menu-console": "Live Console",
        "menu-settings": "Settings",
        "menu-update-badge": "Update",
        "update-badge-title": "Version {version} is available",
        "login-2fa-hint": "Enter the code from your authenticator app. Without your device you can use a recovery code.",
        "login-2fa-label": "Verification code",
        "login-2fa-submit": "Verify",
        "login-2fa-back": "Back to sign in",
        "login-2fa-invalid": "Invalid code.",
        "login-2fa-expired": "The sign in expired. Please enter your password again.",
        "twofa-title": "Two-factor authentication",
        "twofa-desc": "Protects your account with an additional code from an authenticator app (e.g. Google Authenticator).",
        "twofa-status-label": "Status",
        "twofa-status-on": "Enabled ({n} recovery codes left)",
        "twofa-status-off": "Not enabled",
        "twofa-current-password": "Current password",
        "twofa-btn-enable": "Set up",
        "twofa-setup-scan": "Scan the QR code with your authenticator app and enter the code it shows.",
        "twofa-setup-manual": "Or enter the key manually:",
        "twofa-code-label": "Code from the app",
        "twofa-btn-confirm": "Enable",
        "twofa-codes-warning": "Store these recovery codes in a safe place. Each code works exactly once and is only shown now.",
        "twofa-btn-copy": "Copy",
        "twofa-btn-download": "Save as file",
        "twofa-btn-done": "I saved them",
        "twofa-code-or-recovery": "Code from the app or recovery code",
        "twofa-btn-new-codes": "New recovery codes",
        "twofa-btn-disable": "Disable",
        "twofa-enabled": "Two-factor authentication is enabled. Other sessions of your account were signed out.",
        "twofa-disabled": "Two-factor authentication is disabled.",
        "twofa-new-codes": "New recovery codes created. The old ones no longer work.",
        "twofa-confirm-disable": "Disable two-factor authentication for your account?",
        "twofa-copied": "Recovery codes copied.",
        "twofa-copy-failed": "Copying failed, please save the codes as file.",
        "twofa-file-title": "Recovery codes",
        "twofa-btn-reset": "Reset 2FA",
        "twofa-confirm-reset": "Remove two-factor authentication from {user}? The user is signed out and can then sign in with the password only.",
        "update-dev-build": "Development build",
        "update-dev-build-hint": "This is a build from source. Latest release: {version}. Run the installer without --source/--dev to switch to the release.",
        "menu-logout": "Logout",
        "login-title": "LinuxGSM WebAdmin",
        "login-subtitle": "Sign in to manage your game servers.",
        "login-invalid": "Invalid credentials",
        "btn-clear": "Clear",
        "action-failed": "The action failed.",
        "menu-open": "Open menu",
        "theme-toggle": "Toggle theme",
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
        "btn-check-update": "Check Update",
        "btn-mods-install": "Install Mods",
        "btn-mods-update": "Update Mods",
        "btn-mods-remove": "Remove Mods",
        "console-no-input": "This game does not accept console commands (LinuxGSM: consoleinteract=no)",
        "status-warning": "Done with warnings",
        "config-game-badge": "Game",
        "config-game-badge-title": "The config file LinuxGSM starts the game with (servercfgfullpath)",
        "delete-force-confirm": "The server is still running after the stop command (for example players are online and LinuxGSM only stops empty servers). End its session and delete it anyway?",
        "delete-installing": "The server is still being installed. Delete it when the installation has finished.",
        "btn-change-password": "Set ServerAdmin password",
        "btn-fastdl": "FastDL Sync",
        "btn-postdetails": "Support Log (Termbin)",
        "files-btn-upload": "Upload File",
        "firewall-title": "Firewall Inspector & Privilege Probe",
        "firewall-desc": "Checks active host firewall (UFW/Firewalld) status and dashboard root execution rights.",
        "firewall-label-status": "Status & Privileges:",
        "firewall-status-checking": "Checking...",
        "firewall-status-restricted": "Restricted (No Root Privileges)",
        "firewall-status-active": "Active",
        "firewall-status-inactive": "Inactive",
        "firewall-status-unknown": "Unknown",
        "firewall-rules-header": "Detected Rules / Open Ports",
        "bulk-label": "Bulk Actions:",
        "filter-all-tags": "All Clusters / Tags",
        "grid-columns-auto": "Columns: auto",
        "grid-columns-one": "1 column",
        "grid-columns-n": "{n} columns",
        "cron-builder-title": "Visual Crontab Scheduler",
        "cron-builder-desc": "Configure automated interval tasks and scheduled actions for this server.",
        "cron-monitor-label": "Crash-Monitoring Interval",
        "cron-update-label": "Auto-Update Check Interval",
        "cron-restart-label": "Daily Restart / Force-Update Time",
        "cron-core-label": "Weekly LGSM Core Update",
        "btn-save-schedule": "Save Schedule to Crontab",
        "modal-players-title": "Players",
        "col-player-name": "Player",
        "col-player-score": "Score",
        "col-player-time": "Playtime",
        "col-player-actions": "Actions",
        "perm-files": "File Manager",
        "menu-files": "File Manager",
        "menu-audit": "Audit Log",
        "files-title": "File Manager",
        "files-subtitle": "Browse, manage, and edit game server files inside serverfiles/.",
        "files-select-default": "-- Select a server --",
        "files-select-prompt": "Please select a server to browse files.",
        "audit-title": "Security Audit Log",
        "audit-subtitle": "Unfiltered log of administrative actions and user logins.",
        "audit-empty": "No audit entries recorded.",
        "modal-mods-title": "Mods and plugins",
        "modal-mods-desc": "Install or remove the addons and plugins LinuxGSM offers for this game.",
        "mods-loading": "Loading mods list...",
        "mods-installed": "Installed",
        "mods-available": "Available",
        "mods-none-installed": "No mods installed (lgsm/mods/installed-mods.txt).",
        "mods-none-available": "LinuxGSM offers no mods for this game.",
        "mods-load-error": "The mod list could not be loaded.",
        "mods-btn-install": "Install",
        "mods-btn-reinstall": "Reinstall",
        "mods-btn-remove": "Remove",
        "mods-confirm-remove": "Remove {mod}? LinuxGSM deletes the files it installed for this mod.",
        "modal-ts3pw-title": "Set ServerAdmin password",
        "modal-ts3pw-desc": "LinuxGSM restarts the TeamSpeak 3 server with the new ServerQuery password for serveradmin.",
        "ts3pw-password": "New password",
        "ts3pw-confirm": "Repeat password",
        "ts3pw-hint": "8 to 64 characters: letters, digits and . _ + = @ # % ^ * ! ? : , ~ -",
        "ts3pw-mismatch": "The passwords do not match.",
        "ts3pw-invalid": "The password needs 8 to 64 characters: letters, digits and . _ + = @ # % ^ * ! ? : , ~ -",
        "ts3pw-submit": "Set password",
        "alerts-settings-title": "Notification Alerts Settings",
        "alerts-discord-webhook-label": "Discord Webhook URL",
        "settings-tools-btn-systemd": "Install & Enable Service",
        "settings-tools-btn-cron": "Write Cronjobs to Crontab",
        "settings-tools-btn-cron-docker": "Enable Recommended Schedules",
        "btn-refresh": "Refresh",
        "inst-title": "Game Installer",
        "inst-subtitle": "Install a new LinuxGSM server. A system user will be automatically created.",
        "inst-btn-sync": "Update Game List",
        "inst-search-placeholder": "Search games...",
        "inst-user-label": "System Username",
        "inst-name-label": "Server Name",
        "inst-name-help": "Name of the server and its container (3-16 chars: lowercase letters, numbers, underscores). The data is stored in the server data directory under this name.",
        "inst-pass-label": "User Password (Optional)",
        "inst-btn-install": "Install Game Server",
        "settings-title": "Settings & Info",
        "settings-subtitle": "Your account, dashboard settings and server tools.",
        "settings-pass-card": "Change password",
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
        "settings-pass-card-desc": "Change the password of your dashboard account.",
        "settings-tools-card": "Server-specific Tools",
        "settings-tools-card-desc": "Select one of your active servers to show copy-paste templates for systemd and cronjobs.",
        "settings-tools-select-label": "Select server",
        "settings-tools-select-default": "-- Select server --",
        "settings-tools-systemd-title": "Systemd Service (Autostart on Boot)",
        "settings-tools-systemd-desc": "Create a file under /etc/systemd/system/lgsm-[server].service as root and add the following content:",
        "settings-tools-cron-title": "Cronjobs (Scheduled Maintenance Tasks)",
        "settings-tools-cron-desc": "Add these lines to the server user's crontab (or via crontab -e as root):",
        "settings-tools-cron-desc-docker": "In Docker mode the dashboard runs the schedules itself (times in the time zone set under Settings > General). Recommended: monitor every 5 minutes, update check every 30 minutes, force-update at 04:30 and update-lgsm on Sundays.",
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
        "modal-config-default-warning": "Note: LinuxGSM restores _default.cfg every time the server script runs. Changed values are saved to the instance config instead, which overrides _default.cfg.",
        "config-default-redirected": "LinuxGSM restores _default.cfg every time the server script runs, so the changes were saved to {file} instead: {keys}",
        "config-default-no-changes": "No values were changed.",
        "inst-user-placeholder": "e.g. valheimserver",
        "inst-user-help": "Allowed: lowercase letters, numbers, and underscores (3-16 chars). The user will be created on the host.",
        "inst-pass-placeholder": "Leave blank for random password",
        "inst-pass-help": "Password for SSH/system login. Not required by LinuxGSM itself.",
        "inst-dir-label": "Installation directory (optional)",
        "inst-dir-placeholder": "e.g. /mnt/ssd/gameservers/valheim",
        "inst-dir-help": "Leave empty to install into /home/<user>. The directory must not exist yet; its parent (e.g. the mount point of a second SSD) must exist.",
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
        "config-form-empty": "No values found that the form can show. Edit this file in the text view.",
        "config-form-empty-lgsm": "This file does not set any values yet, so LinuxGSM uses the values from _default.cfg. Values you change in the form of _default.cfg are saved here; in the text view you can add settings directly.",
        "config-form-open-default": "Open _default.cfg",
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
        "settings-mode-prod": "PRODUCTION (Linux Host)",
        "settings-mode-docker": "PRODUCTION (Docker)",
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
        "modal-delete-warning": "WARNING: This action is permanent and cannot be undone! It deletes the game files and configs of this server. A Linux user that exists only for this server is deleted together with its home directory; other users are kept.",
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
        "settings-paths-title": "Additional server directories",
        "settings-paths-desc": "LinuxGSM servers outside of /home (e.g. on a separate SSD). Each directory and its direct sub directories are scanned. The owner of a directory is the Linux user the server runs as; directories owned by root are ignored.",
        "settings-paths-label": "One absolute path per line",
        "settings-paths-save": "Save directories",
        "settings-tz-title": "Schedule Time Zone",
        "settings-tz-desc": "In Docker mode the dashboard runs the schedules. Times such as the daily restart or backups use this time zone.",
        "settings-tz-label": "Time zone",
        "settings-tz-now": "Current time:",
        "settings-tz-save": "Save time zone",
        "settings-tz-default": "Default ({zone})",
        "settings-tz-saved": "Schedules now use {zone}.",
        "settings-paths-saved": "Directories saved. Servers are being rescanned.",
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
        "backups-upload-invalid-ext": "Only .tar.gz, .tar.zst and .tar files are allowed.",
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
        "perm-schedule": "Edit schedules (cron)",
        "perm-alerts": "Notifications & webhooks",
        "confirm-delete-backup-title": "Delete Backup",
        "confirm-delete-backup-text": "Are you sure you want to delete this backup? This action cannot be undone.",
        "confirm-restore-backup-title": "Restore Backup",
        "confirm-restore-backup-text": "Are you sure you want to restore this backup? The game server will be stopped and all files will be replaced with the state of this backup.",
        "backups-settings-title": "Backup Settings",
        "backups-setting-maxbackups-label": "Max Backups (maxbackups)",
        "backups-setting-maxbackups-help": "Number of backups to keep (at least 1). LinuxGSM deletes older archives in the backup directory, also those of other instances in the same directory.",
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
        "backups-setting-cron-custom-help": "Format: Minute Hour Day-of-Month Month Day-of-Week",
        "btn-save": "Save",
        "console-mode-game": "Game Log",
        "console-mode-script": "Script Log",
        "console-mode-tmux": "tmux Live Console",
        "role-label": "Role",
        "settings-info-pid": "Process ID (PID)",
        "settings-info-version": "Dashboard Version",
        "user-password-help": "Password for the web login of this user.",
        "settings-btn-cron": "Write Cronjobs to Crontab",
        "settings-btn-systemd": "Auto-Install & Enable Service",
        "settings-confirm-pass": "Confirm New Password",
        "settings-new-pass": "New Password",
        "settings-templates-card": "Server-specific Templates",
        "col-ip": "IP Address",
        "col-action": "Action",
        "col-details": "Details",
        "col-server": "Server",
        "settings-tag-label": "Server Tag / Cluster",
        "settings-tag-placeholder": "e.g. Cluster-1, Public, EU-Server",
        "alerts-telegram-label": "Telegram-Alerts",
        "alerts-telegram-token-label": "Telegram Bot Token",
        "alerts-telegram-chatid-label": "Telegram Chat ID",
        "alerts-email-label": "E-Mail-Alerts",
        "alerts-email-smtp-label": "SMTP Server",
        "alerts-email-port-label": "SMTP Port",
        "alerts-email-user-label": "SMTP User",
        "alerts-email-pass-label": "SMTP Password",
        "alerts-email-dest-label": "Recipient E-Mail",
        "alerts-ntfy-label": "ntfy-Alerts",
        "alerts-ntfy-url-label": "ntfy Server URL",
        "alerts-ntfy-topic-label": "ntfy Topic",
        "alerts-ntfy-token-label": "Auth Token (Optional)",
        "alerts-slack-label": "Slack-Alerts",
        "alerts-slack-webhook-label": "Slack Webhook URL",
        "alerts-matrix-label": "Matrix-Alerts",
        "alerts-matrix-homeserver-label": "Homeserver URL",
        "alerts-matrix-roomid-label": "Room ID",
        "alerts-matrix-token-label": "Access Token",
        "alerts-pushover-label": "Pushover-Alerts",
        "alerts-pushover-token-label": "App Token",
        "alerts-pushover-user-label": "User Key",
        "alerts-pushbullet-label": "Pushbullet-Alerts",
        "alerts-pushbullet-token-label": "Access Token",
        "alerts-pushbullet-channel-label": "Channel Tag (Optional)",
        "alerts-ifttt-label": "IFTTT-Alerts",
        "alerts-ifttt-key-label": "IFTTT Key",
        "alerts-ifttt-event-label": "Event Name",
        "alerts-rocketchat-label": "Rocket.Chat-Alerts",
        "alerts-rocketchat-webhook-label": "Webhook URL",
        "backups-maxbackups-placeholder": "e.g. 4",
        "backups-maxbackupdays-placeholder": "e.g. 30",
        "backups-cron-custom-placeholder": "0 5 * * *",
        "user-username-placeholder": "e.g. supporter_1",
        "tag-save-success": "Server tag saved successfully!",
        "tag-save-error": "Error saving server tag:",
        "tag-prompt": "Enter tag / cluster name for this server (e.g. Cluster-1, Public, Event - or leave blank to remove):",
        "cron-option-5m": "Every 5 minutes",
        "cron-option-10m": "Every 10 minutes",
        "cron-option-15m": "Every 15 minutes",
        "cron-option-30m": "Every 30 minutes",
        "cron-option-1h": "Every hour",
        "cron-option-sunday": "Sundays at 00:00 AM",
        "cron-option-monday": "Mondays at 00:00 AM",
        "cron-option-disabled": "Disabled",
        "btn-debug": "Debug Mode",
        "btn-map-compressor": "Map Compressor",
        "btn-wipe-server": "Wipe Server",
        "settings-stopmode-label": "Stop Mode (Graceful Shutdown)",
        "settings-stopmode-default": "Default (LinuxGSM Default / Normal Stop)",
        "settings-stopmode-1": "Mode 1: tmux kill",
        "settings-stopmode-2": "Mode 2: CTRL+c",
        "settings-stopmode-3": "Mode 3: quit",
        "settings-stopmode-4": "Mode 4: quit 120s",
        "settings-stopmode-5": "Mode 5: stop",
        "settings-stopmode-6": "Mode 6: q",
        "settings-stopmode-7": "Mode 7: exit",
        "settings-stopmode-8": "Mode 8: 7 Days to Die",
        "settings-stopmode-9": "Mode 9: GoldSrc",
        "settings-stopmode-10": "Mode 10: Avorion",
        "settings-stopmode-11": "Mode 11: end",
        "settings-stopmode-12": "Mode 12: shutdown",
        "settings-stopmode-13": "Mode 13: Soulmask",
        "settings-stopmode-14": "Mode 14: Running With Rifles",
        "settings-stopmode-15": "Mode 15: Rising World",
        "settings-stopmode-16": "Mode 16: Conan Exiles",
        "settings-btn-clone": "Clone Instance",
        "cloudbackup-title": "Cloud Backup (rclone Sync)",
        "cloudbackup-btn-sync": "Sync Now",
        "cloudbackup-desc": "Synchronize created backups automatically or manually to cloud storage (Google Drive, AWS S3, Wasabi, Backblaze B2 via rclone).",
        "cloudbackup-enable-label": "Enable Cloud Sync",
        "cloudbackup-remote-label": "rclone Remote Name",
        "cloudbackup-remote-placeholder": "e.g. gdrive or s3",
        "cloudbackup-path-label": "Target Directory Path",
        "cloudbackup-path-placeholder": "backups/linuxgsm",
        "modal-wipe-title": "Rust server wipe",
        "modal-wipe-warning": "A wipe resets the world of the server. Ensure you have created a backup beforehand.",
        "modal-wipe-type-label": "Select Wipe Type:",
        "modal-wipe-map-title": "Map Wipe (map-wipe)",
        "modal-wipe-map-desc": "Deletes all buildings and resources. Player blueprints are retained.",
        "modal-wipe-full-title": "Full Wipe (full-wipe)",
        "modal-wipe-full-desc": "Complete reset: Deletes map, buildings, and all player blueprints.",
        "modal-wipe-btn-submit": "Execute Wipe Now",
        "modal-clone-title": "Clone instance",
        "modal-clone-desc": "Creates a new instance with shared game binaries (symlink), but separate configuration (e.g. for a secondary server on different ports).",
        "modal-clone-suffix-label": "Instance Suffix (Name):",
        "modal-clone-suffix-placeholder": "2",
        "modal-clone-help": "Lowercase letters, numbers, and hyphens only (e.g. '2', 'pvp', 'event').",
        "modal-clone-btn-submit": "Create Instance",
        "stopmode-save-success": "Stop mode saved successfully!",
        "stopmode-save-error": "Failed to save stop mode",
        "clone-success": "Instance cloned successfully! Reloading...",
        "clone-ports-hint": "The clone uses the same ports as the original: set port (and queryport etc.) in its instance config before running both at the same time.",
        "clone-error": "Failed to clone instance",
        "cloudbackup-save-success": "Cloud backup settings saved!",
        "cloudbackup-save-error": "Failed to save cloud backup settings",
        "settings-subtab-general": "General",
        "settings-subtab-server": "Server Tools",
        "settings-subtab-cron": "Crontab & Scheduler",
        "settings-subtab-alerts": "Alerts",
        "settings-subtab-security": "Firewall & Security",
        "settings-subtab-nodes": "Nodes",
        "nodes-add-title": "Add node",
        "nodes-add-desc": "A node is another machine (for example an LXC container or a VPS) whose game servers are managed here as well. The node connects to this dashboard; the connection is encrypted end to end.",
        "nodes-add-name": "Name",
        "nodes-add-mode": "Access",
        "nodes-mode-read-only": "Read only (start/stop allowed)",
        "nodes-mode-read-write": "Read and write",
        "nodes-add-btn": "Create pairing command",
        "nodes-command-intro": "Run this command as root on the node \"{name}\". It is valid once, for {minutes} minutes.",
        "nodes-command-direct": "Alternatively via the direct agent port:",
        "nodes-command-hint": "Afterwards start the agent on the node with \"/opt/lgsm-dashboard/lgsm-dashboard agent run\". The node appears below as soon as it is connected.",
        "nodes-copy": "Copy",
        "nodes-copied": "Copied",
        "nodes-list-title": "Nodes",
        "nodes-refresh": "Refresh",
        "nodes-empty": "No nodes yet.",
        "nodes-col-node": "Node",
        "nodes-col-status": "Status",
        "nodes-col-mode": "Access",
        "nodes-col-version": "Version",
        "nodes-col-host": "Host / last seen",
        "nodes-col-load": "Load",
        "nodes-upgrade-all": "Update all nodes",
        "nodes-upgrade-btn": "Update to {version}",
        "nodes-upgrade-confirm": "Update the node \"{name}\" to {version}? It downloads and verifies the release itself and restarts the agent.",
        "nodes-upgrade-all-confirm": "Update {count} node(s) to {version}, one after the other? Each agent restarts briefly.",
        "nodes-upgrade-node-newer": "Newer than the dashboard: update the dashboard",
        "nodes-upgrade-no-self-update": "Docker: update with docker compose pull && docker compose up -d",
        "nodes-upgrade-disabled": "Updates from the dashboard are disabled on the node (allow_self_update)",
        "nodes-upgrade-unknown": "Build from source: update on the node",
        "nodes-status-online": "Online",
        "nodes-status-offline": "Offline",
        "nodes-status-update": "Update required",
        "nodes-status-problem": "Problem",
        "nodes-effective": "Effective: {mode} (restricted on the node)",
        "nodes-rename": "Rename",
        "nodes-rename-prompt": "New name of the node:",
        "nodes-repair": "Pair again",
        "nodes-repair-confirm": "Create a new pairing command for \"{name}\"? Once it is used, the current installation of the node can no longer connect. Assignments and tags are kept.",
        "nodes-remove": "Remove",
        "nodes-remove-confirm": "Remove the node \"{name}\"? It is disconnected at once, and its user assignments and tags are deleted. The game servers on the node are not touched.",
        "nodes-error": "Error",
        "card-node-offline": "Node offline",
        "card-node-read-only": "Read-only node: start, stop and restart only",
        "users-nodes-label": "Nodes (all servers of the node, including new ones)",
        "inst-target-label": "Install on",
        "inst-target-help": "Read-only nodes and nodes that are not connected are not offered.",
        "inst-target-here": "This machine (dashboard)",
        "fastdl-mode-label": "FastDL served by",
        "fastdl-mode-dashboard": "This dashboard",
        "fastdl-mode-node": "The node itself",
        "fastdl-mode-off": "Off",
        "fastdl-off": "FastDL is off for this server.",
        "fastdl-node-no-url": "The node serves no FastDL yet: start its agent with --fastdl-port and --fastdl-url, or serve public_html/fastdl with your own web server and set --fastdl-url.",
        "fastdl-copied": "FastDL URL copied to clipboard!",
        "settings-server-select-hint": "Settings apply to the selected server instance.",
        "settings-select-server-prompt": "Please select a server above to view server tools and instance settings.",
        "settings-select-server-prompt-cron": "Please select a server above to manage scheduled maintenance tasks.",
        "settings-select-server-prompt-alerts": "Please select a server above to configure notification alerts."
    },
    de: {
        "card-players": "Spieler",
        "card-map": "Karte",
        "card-disk": "Speicher",
        "card-more": "Mehr",
        "card-select": "Server auswählen",
        "card-edit-tag": "Tag bearbeiten",
        "card-measuring": "wird gemessen",
        "card-no-query": "Der Server beantwortet keine Abfragen (nicht unterstützt oder Port blockiert)",
        "card-disk-backups": "Backups",
        "card-disk-free": "Frei auf dem Datenträger",
        "card-disk-shared": "Verzeichnis wird mit anderen Instanzen geteilt",
        "card-disk-pending": "Noch nicht gemessen",
        "card-disk-scanning": "Messung läuft",
        "card-disk-updated": "Gemessen um",
        "card-disk-error": "Unvollständig",
        "overview-col-players": "Spieler",
        "overview-col-disk": "Speicher",
        "menu-dashboard": "Dashboard",
        "menu-installer": "Spiele-Installer",
        "menu-overview": "Übersicht",
        "menu-console": "Live-Konsole",
        "menu-settings": "Einstellungen",
        "menu-update-badge": "Update",
        "update-badge-title": "Version {version} ist verfügbar",
        "login-2fa-hint": "Gib den Code aus deiner Authenticator-App ein. Ohne Gerät kannst du einen Wiederherstellungscode verwenden.",
        "login-2fa-label": "Bestätigungscode",
        "login-2fa-submit": "Bestätigen",
        "login-2fa-back": "Zurück zur Anmeldung",
        "login-2fa-invalid": "Ungültiger Code.",
        "login-2fa-expired": "Die Anmeldung ist abgelaufen. Bitte gib dein Passwort erneut ein.",
        "twofa-title": "Zwei-Faktor-Authentifizierung",
        "twofa-desc": "Schützt dein Konto zusätzlich mit einem Code aus einer Authenticator-App (z.B. Google Authenticator).",
        "twofa-status-label": "Status",
        "twofa-status-on": "Aktiv ({n} Wiederherstellungscodes übrig)",
        "twofa-status-off": "Nicht aktiv",
        "twofa-current-password": "Aktuelles Passwort",
        "twofa-btn-enable": "Einrichten",
        "twofa-setup-scan": "Scanne den QR-Code mit deiner Authenticator-App und gib den angezeigten Code ein.",
        "twofa-setup-manual": "Oder den Schlüssel manuell eingeben:",
        "twofa-code-label": "Code aus der App",
        "twofa-btn-confirm": "Aktivieren",
        "twofa-codes-warning": "Speichere diese Wiederherstellungscodes an einem sicheren Ort. Jeder Code funktioniert genau einmal und wird nur jetzt angezeigt.",
        "twofa-btn-copy": "Kopieren",
        "twofa-btn-download": "Als Datei speichern",
        "twofa-btn-done": "Gespeichert",
        "twofa-code-or-recovery": "Code aus der App oder Wiederherstellungscode",
        "twofa-btn-new-codes": "Neue Wiederherstellungscodes",
        "twofa-btn-disable": "Deaktivieren",
        "twofa-enabled": "Zwei-Faktor-Authentifizierung ist aktiv. Andere Sitzungen deines Kontos wurden abgemeldet.",
        "twofa-disabled": "Zwei-Faktor-Authentifizierung ist deaktiviert.",
        "twofa-new-codes": "Neue Wiederherstellungscodes erstellt. Die alten funktionieren nicht mehr.",
        "twofa-confirm-disable": "Zwei-Faktor-Authentifizierung für dein Konto deaktivieren?",
        "twofa-copied": "Wiederherstellungscodes kopiert.",
        "twofa-copy-failed": "Kopieren fehlgeschlagen, bitte als Datei speichern.",
        "twofa-file-title": "Wiederherstellungscodes",
        "twofa-btn-reset": "2FA zurücksetzen",
        "twofa-confirm-reset": "Zwei-Faktor-Authentifizierung von {user} entfernen? Der Benutzer wird abgemeldet und kann sich danach nur mit dem Passwort anmelden.",
        "update-dev-build": "Entwicklungs-Build",
        "update-dev-build-hint": "Dies ist ein selbst kompilierter Build. Aktuelles Release: {version}. Den Installer ohne --source/--dev ausführen, um zum Release zu wechseln.",
        "menu-logout": "Abmelden",
        "login-title": "LinuxGSM WebAdmin",
        "login-subtitle": "Melde dich an, um deine Gameserver zu verwalten.",
        "login-invalid": "Ungültige Zugangsdaten",
        "btn-clear": "Leeren",
        "action-failed": "Die Aktion ist fehlgeschlagen.",
        "menu-open": "Menü öffnen",
        "theme-toggle": "Theme wechseln",
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
        "btn-check-update": "Update prüfen",
        "btn-mods-install": "Mods Installieren",
        "btn-mods-update": "Mods Updaten",
        "btn-mods-remove": "Mods entfernen",
        "console-no-input": "Dieses Spiel nimmt keine Konsolenbefehle an (LinuxGSM: consoleinteract=no)",
        "status-warning": "Fertig mit Warnungen",
        "config-game-badge": "Spiel",
        "config-game-badge-title": "Die Konfigurationsdatei, mit der LinuxGSM das Spiel startet (servercfgfullpath)",
        "delete-force-confirm": "Der Server läuft nach dem Stopp-Befehl noch (zum Beispiel sind Spieler online und LinuxGSM stoppt nur leere Server). Sitzung beenden und trotzdem löschen?",
        "delete-installing": "Der Server wird noch installiert. Lösche ihn, wenn die Installation abgeschlossen ist.",
        "btn-change-password": "ServerAdmin-Passwort setzen",
        "btn-fastdl": "FastDL Sync",
        "btn-postdetails": "Support Log (Termbin)",
        "files-btn-upload": "Datei hochladen",
        "firewall-title": "Firewall Inspector & Rechte-Prüfung",
        "firewall-desc": "Prüft den Firewall-Status (UFW/Firewalld) und Root-Berechtigungen des Dashboards.",
        "firewall-label-status": "Status & Rechte:",
        "firewall-status-checking": "Prüfe...",
        "firewall-status-restricted": "Eingeschränkt (Keine Root-Rechte)",
        "firewall-status-active": "Aktiv",
        "firewall-status-inactive": "Inaktiv",
        "firewall-status-unknown": "Unbekannt",
        "firewall-rules-header": "Erkannte Regeln / Offene Ports",
        "bulk-label": "Massen-Aktionen:",
        "filter-all-tags": "Alle Clusters / Tags",
        "grid-columns-auto": "Spalten: Auto",
        "grid-columns-one": "1 Spalte",
        "grid-columns-n": "{n} Spalten",
        "cron-builder-title": "Visueller Crontab-Scheduler",
        "cron-builder-desc": "Konfiguriere automatische Intervall-Aufgaben für diesen Server.",
        "cron-monitor-label": "Crash-Monitoring Intervall",
        "cron-update-label": "Auto-Update Check Intervall",
        "cron-restart-label": "Täglicher Neustart / Force-Update",
        "cron-core-label": "Wöchentliches LGSM Update",
        "btn-save-schedule": "Zeitplan in Crontab speichern",
        "modal-players-title": "Spieler",
        "col-player-name": "Spieler",
        "col-player-score": "Score",
        "col-player-time": "Spieldauer",
        "col-player-actions": "Aktionen",
        "perm-files": "Dateimanager",
        "menu-files": "Dateimanager",
        "menu-audit": "Audit-Log",
        "files-title": "Dateimanager",
        "files-subtitle": "Durchsuche, verwalte und bearbeite Spieldateien in serverfiles/.",
        "files-select-default": "-- Server wählen --",
        "files-select-prompt": "Bitte wähle einen Server aus, um Dateien anzuzeigen.",
        "audit-title": "Sicherheits Audit-Log",
        "audit-subtitle": "Ungefiltertes Protokoll aller administrativen Aktionen und System-Anmeldungen.",
        "audit-empty": "Keine Audit-Einträge vorhanden.",
        "modal-mods-title": "Mods und Plugins",
        "modal-mods-desc": "Installiere oder entferne die Addons und Plugins, die LinuxGSM für dieses Spiel anbietet.",
        "mods-loading": "Lade Mod-Liste...",
        "mods-installed": "Installiert",
        "mods-available": "Verfügbar",
        "mods-none-installed": "Keine Mods installiert (lgsm/mods/installed-mods.txt).",
        "mods-none-available": "LinuxGSM bietet für dieses Spiel keine Mods an.",
        "mods-load-error": "Die Mod-Liste konnte nicht geladen werden.",
        "mods-btn-install": "Installieren",
        "mods-btn-reinstall": "Neu installieren",
        "mods-btn-remove": "Entfernen",
        "mods-confirm-remove": "{mod} entfernen? LinuxGSM löscht die Dateien, die es für diese Mod installiert hat.",
        "modal-ts3pw-title": "ServerAdmin-Passwort setzen",
        "modal-ts3pw-desc": "LinuxGSM startet den TeamSpeak-3-Server mit dem neuen ServerQuery-Passwort für serveradmin neu.",
        "ts3pw-password": "Neues Passwort",
        "ts3pw-confirm": "Passwort wiederholen",
        "ts3pw-hint": "8 bis 64 Zeichen: Buchstaben, Ziffern und . _ + = @ # % ^ * ! ? : , ~ -",
        "ts3pw-mismatch": "Die Passwörter stimmen nicht überein.",
        "ts3pw-invalid": "Das Passwort braucht 8 bis 64 Zeichen: Buchstaben, Ziffern und . _ + = @ # % ^ * ! ? : , ~ -",
        "ts3pw-submit": "Passwort setzen",
        "alerts-settings-title": "Benachrichtigungs-Einstellungen",
        "alerts-discord-webhook-label": "Discord Webhook URL",
        "settings-tools-btn-systemd": "Dienst automatisch installieren & aktivieren",
        "settings-tools-btn-cron": "Cronjobs in Crontab schreiben",
        "settings-tools-btn-cron-docker": "Empfohlene Zeitpläne aktivieren",
        "btn-refresh": "Aktualisieren",
        "inst-title": "Spiele-Installer",
        "inst-subtitle": "Installiere einen neuen LinuxGSM Server. Es wird automatisch ein neuer Systembenutzer angelegt.",
        "inst-btn-sync": "Spieleliste aktualisieren",
        "inst-search-placeholder": "Spiele durchsuchen...",
        "inst-user-label": "System-Benutzername",
        "inst-name-label": "Servername",
        "inst-name-help": "Name des Servers und seines Containers (3-16 Zeichen: Kleinbuchstaben, Zahlen, Unterstriche). Die Daten liegen unter diesem Namen im Server-Datenverzeichnis.",
        "inst-pass-label": "Passwort (Optional)",
        "inst-btn-install": "Gameserver installieren",
        "settings-title": "Einstellungen & Info",
        "settings-subtitle": "Dein Konto, Dashboard-Einstellungen und Server-Werkzeuge.",
        "settings-pass-card": "Passwort ändern",
        "settings-pass-card-desc": "Ändere das Passwort deines Dashboard-Kontos.",
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
        "settings-tools-cron-desc-docker": "Im Docker-Modus führt das Dashboard die Zeitpläne selbst aus (Uhrzeiten in der Zeitzone unter Einstellungen > Allgemein). Empfohlen: monitor alle 5 Minuten, Update-Prüfung alle 30 Minuten, force-update um 04:30 und update-lgsm sonntags.",
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
        "modal-config-default-warning": "Hinweis: LinuxGSM stellt _default.cfg bei jedem Aufruf des Server-Scripts wieder her. Geänderte Werte werden deshalb in der Instanz-Config gespeichert, die _default.cfg überschreibt.",
        "config-default-redirected": "LinuxGSM stellt _default.cfg bei jedem Aufruf des Server-Scripts wieder her, deshalb wurden die Änderungen in {file} gespeichert: {keys}",
        "config-default-no-changes": "Es wurden keine Werte geändert.",
        "inst-user-placeholder": "z.B. valheimserver",
        "inst-user-help": "Erlaubt sind Kleinbuchstaben, Zahlen und Unterstriche (3-16 Zeichen). Der Benutzer wird auf dem Server angelegt.",
        "inst-pass-placeholder": "Frei lassen für zufälliges Passwort",
        "inst-pass-help": "Passwort für den SSH/System-Login des Benutzers. Wird für LinuxGSM selbst nicht benötigt.",
        "inst-dir-label": "Installationsverzeichnis (optional)",
        "inst-dir-placeholder": "z.B. /mnt/ssd/gameserver/valheim",
        "inst-dir-help": "Leer lassen für /home/<benutzer>. Das Verzeichnis darf noch nicht existieren, der übergeordnete Ordner (z.B. der Mountpoint einer zweiten SSD) muss existieren.",
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
        "config-form-empty": "Keine Werte gefunden, die das Formular anzeigen kann. Bearbeite die Datei in der Textansicht.",
        "config-form-empty-lgsm": "Diese Datei setzt noch keine Werte, LinuxGSM nutzt daher die Werte aus _default.cfg. Werte, die du im Formular von _default.cfg änderst, werden hier gespeichert; in der Textansicht kannst du Einstellungen direkt eintragen.",
        "config-form-open-default": "_default.cfg öffnen",
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
        "settings-mode-prod": "PRODUKTION (Linux Root)",
        "settings-mode-docker": "PRODUKTION (Docker)",
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
        "modal-delete-warning": "WARNUNG: Diese Aktion kann nicht rückgängig gemacht werden! Die Spieldateien und Konfigurationen dieses Servers werden gelöscht. Ein Linux-Benutzer, der nur für diesen Server existiert, wird samt Home-Verzeichnis gelöscht; andere Benutzer bleiben erhalten.",
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
        "settings-paths-title": "Zusätzliche Server-Verzeichnisse",
        "settings-paths-desc": "LinuxGSM-Server außerhalb von /home (z.B. auf einer separaten SSD). Jedes Verzeichnis und seine direkten Unterordner werden durchsucht. Der Besitzer eines Verzeichnisses ist der Linux-Benutzer, unter dem der Server läuft; Verzeichnisse von root werden ignoriert.",
        "settings-paths-label": "Ein absoluter Pfad pro Zeile",
        "settings-paths-save": "Verzeichnisse speichern",
        "settings-tz-title": "Zeitzone der Zeitpläne",
        "settings-tz-desc": "Im Docker-Modus führt das Dashboard die Zeitpläne aus. Uhrzeiten wie der tägliche Neustart oder Backups gelten in dieser Zeitzone.",
        "settings-tz-label": "Zeitzone",
        "settings-tz-now": "Aktuelle Zeit:",
        "settings-tz-save": "Zeitzone speichern",
        "settings-tz-default": "Standard ({zone})",
        "settings-tz-saved": "Zeitpläne nutzen jetzt {zone}.",
        "settings-paths-saved": "Verzeichnisse gespeichert. Server werden neu eingelesen.",
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
        "backups-upload-invalid-ext": "Nur .tar.gz-, .tar.zst- und .tar-Dateien sind erlaubt.",
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
        "perm-schedule": "Zeitpläne bearbeiten (Cron)",
        "perm-alerts": "Benachrichtigungen & Webhooks",
        "confirm-delete-backup-title": "Backup löschen",
        "confirm-delete-backup-text": "Möchtest du dieses Backup wirklich löschen? Dieser Schritt kann nicht rückgängig gemacht werden.",
        "confirm-restore-backup-title": "Backup wiederherstellen",
        "confirm-restore-backup-text": "Möchtest du dieses Backup wirklich wiederherstellen? Der Gameserver wird gestoppt und alle Spieldateien werden auf den Stand dieses Backups zurückgesetzt.",
        "backups-settings-title": "Backup-Einstellungen",
        "backups-setting-maxbackups-label": "Max Backups (maxbackups)",
        "backups-setting-maxbackups-help": "Maximale Anzahl an Backups, die behalten werden (mindestens 1). LinuxGSM löscht ältere Archive im Backup-Ordner, auch die anderer Instanzen im selben Verzeichnis.",
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
        "backups-setting-cron-custom-help": "Format: Minute Stunde Tag-des-Monats Monat Tag-der-Woche",
        "btn-save": "Speichern",
        "console-mode-game": "Spiel-Log",
        "console-mode-script": "Skript-Log",
        "console-mode-tmux": "tmux Live-Konsole",
        "role-label": "Rolle",
        "settings-info-pid": "Prozess-ID (PID)",
        "settings-info-version": "Dashboard Version",
        "user-password-help": "Passwort für den Web-Login dieses Benutzers.",
        "settings-btn-cron": "Cronjobs in Crontab schreiben",
        "settings-btn-systemd": "Dienst automatisch installieren & aktivieren",
        "settings-confirm-pass": "Neues Passwort bestätigen",
        "settings-new-pass": "Neues Passwort",
        "settings-templates-card": "Server-spezifische Vorlagen",
        "col-ip": "IP-Adresse",
        "col-action": "Aktion",
        "col-details": "Details",
        "col-server": "Server",
        "settings-tag-label": "Server Tag / Cluster",
        "settings-tag-placeholder": "z. B. Cluster-1, Public, EU-Server",
        "alerts-telegram-label": "Telegram-Alerts",
        "alerts-telegram-token-label": "Telegram Bot Token",
        "alerts-telegram-chatid-label": "Telegram Chat ID",
        "alerts-email-label": "E-Mail-Alerts",
        "alerts-email-smtp-label": "SMTP Server",
        "alerts-email-port-label": "SMTP Port",
        "alerts-email-user-label": "SMTP Benutzer",
        "alerts-email-pass-label": "SMTP Passwort",
        "alerts-email-dest-label": "Empfänger E-Mail",
        "alerts-ntfy-label": "ntfy-Alerts",
        "alerts-ntfy-url-label": "ntfy Server URL",
        "alerts-ntfy-topic-label": "ntfy Topic",
        "alerts-ntfy-token-label": "Auth Token (Optional)",
        "alerts-slack-label": "Slack-Alerts",
        "alerts-slack-webhook-label": "Slack Webhook URL",
        "alerts-matrix-label": "Matrix-Alerts",
        "alerts-matrix-homeserver-label": "Homeserver URL",
        "alerts-matrix-roomid-label": "Room ID",
        "alerts-matrix-token-label": "Access Token",
        "alerts-pushover-label": "Pushover-Alerts",
        "alerts-pushover-token-label": "App Token",
        "alerts-pushover-user-label": "User Key",
        "alerts-pushbullet-label": "Pushbullet-Alerts",
        "alerts-pushbullet-token-label": "Access Token",
        "alerts-pushbullet-channel-label": "Channel Tag (Optional)",
        "alerts-ifttt-label": "IFTTT-Alerts",
        "alerts-ifttt-key-label": "IFTTT Key",
        "alerts-ifttt-event-label": "Event Name",
        "alerts-rocketchat-label": "Rocket.Chat-Alerts",
        "alerts-rocketchat-webhook-label": "Webhook URL",
        "backups-maxbackups-placeholder": "z.B. 4",
        "backups-maxbackupdays-placeholder": "z.B. 30",
        "backups-cron-custom-placeholder": "0 5 * * *",
        "user-username-placeholder": "z.B. supporter_1",
        "tag-save-success": "Server-Tag erfolgreich gespeichert!",
        "tag-save-error": "Fehler beim Speichern des Server-Tags:",
        "tag-prompt": "Gib den Tag / Cluster-Namen für diesen Server ein (z. B. Cluster-1, Public, Event - oder leer lassen zum Entfernen):",
        "cron-option-5m": "Alle 5 Minuten",
        "cron-option-10m": "Alle 10 Minuten",
        "cron-option-15m": "Alle 15 Minuten",
        "cron-option-30m": "Alle 30 Minuten",
        "cron-option-1h": "Jede Stunde",
        "cron-option-sunday": "Sonntags um 00:00 Uhr",
        "cron-option-monday": "Montags um 00:00 Uhr",
        "cron-option-disabled": "Deaktiviert",
        "btn-debug": "Debug Modus",
        "btn-map-compressor": "Map Compressor",
        "btn-wipe-server": "Wipe Server",
        "settings-stopmode-label": "Stop-Modus (Graceful Shutdown)",
        "settings-stopmode-default": "Standard (LinuxGSM Default / Normaler Stop)",
        "settings-stopmode-1": "Modus 1: tmux kill",
        "settings-stopmode-2": "Modus 2: CTRL+c",
        "settings-stopmode-3": "Modus 3: quit",
        "settings-stopmode-4": "Modus 4: quit 120s",
        "settings-stopmode-5": "Modus 5: stop",
        "settings-stopmode-6": "Modus 6: q",
        "settings-stopmode-7": "Modus 7: exit",
        "settings-stopmode-8": "Modus 8: 7 Days to Die",
        "settings-stopmode-9": "Modus 9: GoldSrc",
        "settings-stopmode-10": "Modus 10: Avorion",
        "settings-stopmode-11": "Modus 11: end",
        "settings-stopmode-12": "Modus 12: shutdown",
        "settings-stopmode-13": "Modus 13: Soulmask",
        "settings-stopmode-14": "Modus 14: Running With Rifles",
        "settings-stopmode-15": "Modus 15: Rising World",
        "settings-stopmode-16": "Modus 16: Conan Exiles",
        "settings-btn-clone": "Instanz klonen",
        "cloudbackup-title": "Cloud Backup (rclone Sync)",
        "cloudbackup-btn-sync": "Jetzt synchronisieren",
        "cloudbackup-desc": "Synchronisiere erstellte Backups automatisch oder manuell mit Cloud-Speichern (Google Drive, AWS S3, Wasabi, Backblaze B2 via rclone).",
        "cloudbackup-enable-label": "Cloud Sync aktivieren",
        "cloudbackup-remote-label": "rclone Remote Name",
        "cloudbackup-remote-placeholder": "z. B. gdrive oder s3",
        "cloudbackup-path-label": "Ziel-Ordnerpfad",
        "cloudbackup-path-placeholder": "backups/linuxgsm",
        "modal-wipe-title": "Rust-Server-Wipe",
        "modal-wipe-warning": "Ein Wipe setzt die Welt des Servers zurück. Stelle sicher, dass du vorher ein Backup angelegt hast.",
        "modal-wipe-type-label": "Wipe-Typ wählen:",
        "modal-wipe-map-title": "Map Wipe (map-wipe)",
        "modal-wipe-map-desc": "Löscht alle Gebäude und Ressourcen. Spieler-Blueprints bleiben erhalten.",
        "modal-wipe-full-title": "Full Wipe (full-wipe)",
        "modal-wipe-full-desc": "Kompletter Reset: Löscht Map, Gebäude und alle Spieler-Blueprints.",
        "modal-wipe-btn-submit": "Wipe jetzt ausführen",
        "modal-clone-title": "Instanz klonen",
        "modal-clone-desc": "Erstellt eine neue Instanz mit geteilten Serverdateien (Symlink), aber eigener Konfiguration (z. B. für einen zweiten Server auf anderen Ports).",
        "modal-clone-suffix-label": "Instanz-Suffix (Name):",
        "modal-clone-suffix-placeholder": "2",
        "modal-clone-help": "Nur Kleinbuchstaben, Zahlen und Bindestriche (z. B. '2', 'pvp', 'event').",
        "modal-clone-btn-submit": "Instanz erstellen",
        "stopmode-save-success": "Stop-Modus erfolgreich gespeichert!",
        "stopmode-save-error": "Fehler beim Speichern des Stop-Modus",
        "clone-success": "Instanz erfolgreich geklont! Aktualisiere...",
        "clone-ports-hint": "Der Klon nutzt dieselben Ports wie das Original: Setze port (und queryport usw.) in seiner Instanz-Config, bevor beide gleichzeitig laufen.",
        "clone-error": "Fehler beim Klonen der Instanz",
        "cloudbackup-save-success": "Cloud-Backup Einstellungen gespeichert!",
        "cloudbackup-save-error": "Fehler beim Speichern der Cloud-Backup Einstellungen",
        "settings-subtab-general": "Allgemein",
        "settings-subtab-server": "Server-Tools",
        "settings-subtab-cron": "Crontab & Scheduler",
        "settings-subtab-alerts": "Benachrichtigungen",
        "settings-subtab-security": "Firewall & Sicherheit",
        "settings-subtab-nodes": "Nodes",
        "nodes-add-title": "Node hinzufügen",
        "nodes-add-desc": "Ein Node ist ein weiterer Rechner (z. B. ein LXC-Container oder ein VPS), dessen Gameserver hier mit verwaltet werden. Der Node verbindet sich mit diesem Dashboard; die Verbindung ist Ende-zu-Ende verschlüsselt.",
        "nodes-add-name": "Name",
        "nodes-add-mode": "Zugriff",
        "nodes-mode-read-only": "Nur lesen (Start/Stop erlaubt)",
        "nodes-mode-read-write": "Lesen und schreiben",
        "nodes-add-btn": "Pairing-Befehl erstellen",
        "nodes-command-intro": "Führe diesen Befehl als root auf dem Node \"{name}\" aus. Er gilt einmal und {minutes} Minuten lang.",
        "nodes-command-direct": "Alternativ über den direkten Agent-Port:",
        "nodes-command-hint": "Danach den Agent auf dem Node mit \"/opt/lgsm-dashboard/lgsm-dashboard agent run\" starten. Der Node erscheint unten, sobald er verbunden ist.",
        "nodes-copy": "Kopieren",
        "nodes-copied": "Kopiert",
        "nodes-list-title": "Nodes",
        "nodes-refresh": "Aktualisieren",
        "nodes-empty": "Noch keine Nodes.",
        "nodes-col-node": "Node",
        "nodes-col-status": "Status",
        "nodes-col-mode": "Zugriff",
        "nodes-col-version": "Version",
        "nodes-col-host": "Host / zuletzt gesehen",
        "nodes-col-load": "Auslastung",
        "nodes-upgrade-all": "Alle Nodes aktualisieren",
        "nodes-upgrade-btn": "Auf {version} aktualisieren",
        "nodes-upgrade-confirm": "Node \"{name}\" auf {version} aktualisieren? Er lädt und prüft das Release selbst und startet den Agent neu.",
        "nodes-upgrade-all-confirm": "{count} Node(s) nacheinander auf {version} aktualisieren? Jeder Agent startet kurz neu.",
        "nodes-upgrade-node-newer": "Neuer als das Dashboard: Dashboard aktualisieren",
        "nodes-upgrade-no-self-update": "Docker: mit docker compose pull && docker compose up -d aktualisieren",
        "nodes-upgrade-disabled": "Updates vom Dashboard sind auf dem Node aus (allow_self_update)",
        "nodes-upgrade-unknown": "Build aus dem Quellcode: auf dem Node aktualisieren",
        "nodes-status-online": "Online",
        "nodes-status-offline": "Offline",
        "nodes-status-update": "Update nötig",
        "nodes-status-problem": "Problem",
        "nodes-effective": "Effektiv: {mode} (auf dem Node eingeschränkt)",
        "nodes-rename": "Umbenennen",
        "nodes-rename-prompt": "Neuer Name des Nodes:",
        "nodes-repair": "Neu koppeln",
        "nodes-repair-confirm": "Neuen Pairing-Befehl für \"{name}\" erstellen? Sobald er benutzt wird, kann sich die bisherige Installation des Nodes nicht mehr verbinden. Zuweisungen und Tags bleiben erhalten.",
        "nodes-remove": "Entfernen",
        "nodes-remove-confirm": "Node \"{name}\" entfernen? Er wird sofort getrennt, seine Benutzerzuweisungen und Tags werden gelöscht. Die Gameserver auf dem Node bleiben unberührt.",
        "nodes-error": "Fehler",
        "card-node-offline": "Node offline",
        "card-node-read-only": "Read-only-Node: nur Start, Stop und Restart",
        "users-nodes-label": "Nodes (alle Server des Nodes, auch neue)",
        "inst-target-label": "Installieren auf",
        "inst-target-help": "Read-only-Nodes und Nodes ohne Verbindung stehen nicht zur Auswahl.",
        "inst-target-here": "Dieser Rechner (Dashboard)",
        "fastdl-mode-label": "FastDL ausgeliefert von",
        "fastdl-mode-dashboard": "Diesem Dashboard",
        "fastdl-mode-node": "Dem Node selbst",
        "fastdl-mode-off": "Aus",
        "fastdl-off": "FastDL ist für diesen Server aus.",
        "fastdl-node-no-url": "Der Node liefert noch kein FastDL aus: Agent mit --fastdl-port und --fastdl-url starten, oder public_html/fastdl mit einem eigenen Webserver ausliefern und --fastdl-url setzen.",
        "fastdl-copied": "FastDL-URL in die Zwischenablage kopiert!",
        "settings-server-select-hint": "Einstellungen gelten für die ausgewählte Server-Instanz.",
        "settings-select-server-prompt": "Bitte wähle oben einen Server aus, um Server-Tools und Instanz-Einstellungen anzuzeigen.",
        "settings-select-server-prompt-cron": "Bitte wähle oben einen Server aus, um geplante Wartungsaufgaben zu verwalten.",
        "settings-select-server-prompt-alerts": "Bitte wähle oben einen Server aus, um Benachrichtigungen zu konfigurieren."
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

const GRID_COLUMN_CHOICES = [1, 2, 3, 4, 5, 6, 7, 8, 10, 12];

// Card grids with a column picker: the server cards on the dashboard and the game list of the
// installer. Each remembers its own choice.
const COLUMN_GRIDS = {
    servers: { select: 'grid-columns-select', grid: 'servers-container', wideClass: 'dashboard-wide', storage: 'gridColumns' },
    games: { select: 'games-columns-select', grid: 'games-container', wideClass: 'installer-wide', storage: 'gameColumns' }
};

function initGridColumns() {
    Object.entries(COLUMN_GRIDS).forEach(([name, cfg]) => {
        const select = document.getElementById(cfg.select);
        if (select) select.addEventListener('change', () => setGridColumns(name, select.value));
        let saved = 'auto';
        try { saved = localStorage.getItem(cfg.storage) || 'auto'; } catch (e) {}
        setGridColumns(name, GRID_COLUMN_CHOICES.includes(parseInt(saved, 10)) ? saved : 'auto');
    });
}

function renderGridColumnsOptions() {
    const options = [`<option value="auto">${escapeHtml(t('grid-columns-auto'))}</option>`];
    GRID_COLUMN_CHOICES.forEach(n => {
        options.push(`<option value="${n}">${escapeHtml(t(n === 1 ? 'grid-columns-one' : 'grid-columns-n').replace('{n}', n))}</option>`);
    });
    Object.entries(COLUMN_GRIDS).forEach(([name, cfg]) => {
        const select = document.getElementById(cfg.select);
        if (!select) return;
        select.innerHTML = options.join('');
        select.value = state.gridColumns[name] || 'auto';
    });
}

// A fixed column count also lifts the content width limit, so wide screens fit more cards.
function setGridColumns(name, value) {
    const cfg = COLUMN_GRIDS[name];
    state.gridColumns[name] = String(value);
    try { localStorage.setItem(cfg.storage, state.gridColumns[name]); } catch (e) {}
    const grid = document.getElementById(cfg.grid);
    const fixed = state.gridColumns[name] !== 'auto';
    if (grid) {
        grid.classList.toggle('fixed-cols', fixed);
        if (fixed) grid.style.setProperty('--grid-cols', state.gridColumns[name]);
        else grid.style.removeProperty('--grid-cols');
    }
    document.body.classList.toggle(cfg.wideClass, fixed);
    const select = document.getElementById(cfg.select);
    if (select) select.value = state.gridColumns[name];
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

    document.querySelectorAll('[data-i18n-title]').forEach(element => {
        const key = element.getAttribute('data-i18n-title');
        if (i18n[lang] && i18n[lang][key]) {
            element.title = i18n[lang][key];
        }
    });

    document.querySelectorAll('[data-i18n-aria-label]').forEach(element => {
        const key = element.getAttribute('data-i18n-aria-label');
        if (i18n[lang] && i18n[lang][key]) {
            element.setAttribute('aria-label', i18n[lang][key]);
        }
    });

    // Re-render all dynamic components in the active language
    renderGridColumnsOptions();
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
        const res = await apiFetch('/api/games/sync', { method: 'POST' });
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
                    ${Number(probe.port) || 0} <span style="font-size: 0.7rem; color: var(--text-muted); display: block; margin-top: 2px;">${escapeHtml(probe.description)}</span>
                </td>
                <td style="padding: 0.75rem 0.5rem; text-align: left; font-size: 0.8rem;">
                    ${escapeHtml(probe.protocol)}
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
        const url = `/api/servers/${encodeURIComponent(serverId)}/delete`;
        let res = await apiFetch(url, { method: 'POST' });
        if (res.status === 409) {
            // Still running after the stop (players online with stoponlyifnoplayers, hung
            // server) or still installing.
            const data = await res.json().catch(() => ({}));
            if (data.code === 'still_running' && confirm(t('delete-force-confirm'))) {
                res = await apiFetch(url + '?force=1', { method: 'POST' });
            } else {
                alert(data.code === 'installing' ? t('delete-installing') : (data.error || t('status-error')));
                el.btnDeleteConfirmSubmit.disabled = false;
                return;
            }
        }
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

// -------------------------------------------------------------
// Update notice (sidebar badge for administrators)
// -------------------------------------------------------------

let updateStatusInterval = null;

function applyUpdateStatus(update) {
    const badge = document.getElementById('menu-update-badge');
    if (!badge) return;
    const show = !!(update && update.has_update && state.currentUser && state.currentUser.role === 'admin');
    badge.classList.toggle('hidden', !show);
    badge.title = show ? t('update-badge-title').replace('{version}', update.latest_version) : '';
}

// The server checks GitHub every few hours; the page only re-reads that cached result.
function startUpdateStatusPolling() {
    if (updateStatusInterval || !state.currentUser || state.currentUser.role !== 'admin') return;
    updateStatusInterval = setInterval(async () => {
        try {
            const res = await fetch('/api/system/info');
            if (res.status === 200) applyUpdateStatus((await res.json()).update);
        } catch (e) {
            // offline: keep the current state
        }
    }, 30 * 60 * 1000);
}

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
        applyUpdateStatus(data);
        if (data.development_build) {
            statusText.textContent = t('update-dev-build');
            statusText.className = 'info-value text-muted';
            detailsRow.classList.add('hidden');
            btnTrigger.classList.add('hidden');
            if (manual) {
                messageArea.textContent = t('update-dev-build-hint').replace('{version}', data.latest_version || '-');
                messageArea.className = 'info-message text-muted';
            }
            return;
        }
        
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
        alert(state.language === 'de' ? 'Kein Update-Tag vorhanden.' : 'No update tag available.');
        return;
    }
    
    if (!confirm(state.language === 'de' ? 'Möchtest du das Dashboard wirklich aktualisieren? Die Verbindung wird kurz getrennt.' : 'Are you sure you want to update the dashboard? The connection will be briefly disconnected.')) {
        return;
    }
    
    messageArea.textContent = state.language === 'de' ? 'Update wird heruntergeladen und geprüft... Bitte warten.' : 'Downloading and verifying the update... Please wait.';
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
                
            let servers = user.role === 'admin' ? 
                `<span class="text-muted" style="font-size: 0.8rem;">${state.language === 'de' ? 'Alle Server' : 'All Servers'}</span>` : 
                (user.servers && user.servers.length > 0 ? 
                    user.servers.map(s => `<code style="font-size: 0.8rem;">${escapeHtml(s)}</code>`).join(', ') : 
                    `<span class="text-muted" style="font-size: 0.8rem;">${state.language === 'de' ? 'Keine' : 'None'}</span>`);
                    
            if (user.role !== 'admin' && user.nodes && user.nodes.length > 0) {
                const chips = user.nodes.map(n => `<span class="node-chip">${escapeHtml(n)}</span>`).join(' ');
                servers = user.servers && user.servers.length > 0 ? `${servers} ${chips}` : chips;
            }
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
                            if (p === 'files') label = 'Dateimanager';
                            if (p === 'backup') label = 'Backups';
                            if (p === 'schedule') label = 'Zeitpläne';
                            if (p === 'alerts') label = 'Benachrichtigungen';
                        }
                        return `<span class="badge badge-pulse" style="font-size: 0.7rem; background: rgba(255,255,255,0.05); color: var(--text-color); border: 1px solid var(--border-color);">${escapeHtml(label)}</span>`;
                    }).join(' ') : 
                    `<span class="text-muted" style="font-size: 0.8rem;">${state.language === 'de' ? 'Keine' : 'None'}</span>`);
            
            const isSelf = state.currentUser && state.currentUser.username === user.username;
            const deleteBtn = isSelf ? 
                `<button class="btn btn-danger btn-sm" disabled style="opacity: 0.4;">${state.language === 'de' ? 'Löschen' : 'Delete'}</button>` : 
                `<button class="btn btn-danger btn-sm" onclick="deleteUser(${jsArg(user.username)})">${state.language === 'de' ? 'Löschen' : 'Delete'}</button>`;
                
            tr.innerHTML = `
                <td style="padding: 0.75rem;"><strong>${escapeHtml(user.username)}</strong> ${isSelf ? `<small class="text-muted">(${state.language === 'de' ? 'Du' : 'You'})</small>` : ''} ${user.totp_enabled ? `<span class="badge badge-primary" title="${t('twofa-title')}">2FA</span>` : ''}</td>
                <td style="padding: 0.75rem;">${roleBadge}</td>
                <td style="padding: 0.75rem;">${servers}</td>
                <td style="padding: 0.75rem;">${perms}</td>
                <td style="padding: 0.75rem; text-align: right; display: flex; gap: 0.5rem; justify-content: flex-end;">
                    ${user.totp_enabled ? `<button class="btn btn-secondary btn-sm" onclick="resetUserTwoFactor(${jsArg(user.username)})">${t('twofa-btn-reset')}</button>` : ''}
                    <button class="btn btn-secondary btn-sm" onclick="openEditUserModal(${jsArg(user.username)})">${state.language === 'de' ? 'Bearbeiten' : 'Edit'}</button>
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
                
                const node = srv.node ? ` · ${escapeHtml(srv.node_name || srv.node)}` : '';
                label.innerHTML = `
                    <input type="checkbox" name="user-assigned-servers" value="${escapeHtml(srv.id)}">
                    <span>${escapeHtml(srv.name)} (<code>${escapeHtml(srv.user)}</code>)${node}</span>
                `;
                listDiv.appendChild(label);
            });
        }
    } catch (e) {
        console.error('Failed to load servers for user form:', e);
    }
    await populateUserNodeCheckboxes();
}

// populateUserNodeCheckboxes lists the nodes a user can be assigned to as a whole.
async function populateUserNodeCheckboxes() {
    const section = document.getElementById('user-nodes-section');
    const list = document.getElementById('user-nodes-list');
    if (!section || !list) return;
    list.innerHTML = '';
    let nodes = [];
    try {
        const res = await apiFetch('/api/admin/nodes');
        if (res.ok) nodes = (await res.json()).nodes || [];
    } catch (e) {
        nodes = [];
    }
    section.classList.toggle('hidden', nodes.length === 0);
    nodes.forEach(node => {
        const label = document.createElement('label');
        label.className = 'checkbox-label';
        label.style.cssText = 'display:flex; align-items:center; gap:0.5rem; cursor:pointer; font-size:0.85rem;';
        label.innerHTML = `
            <input type="checkbox" name="user-assigned-nodes" value="${escapeHtml(node.id)}">
            <span>${escapeHtml(node.name)} (<code>${escapeHtml(node.id)}</code>)</span>
        `;
        list.appendChild(label);
    });
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
            document.querySelectorAll('input[name="user-assigned-nodes"]').forEach(cb => {
                cb.checked = !!(user.nodes && user.nodes.includes(cb.value));
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
    const nodes = [];
    if (role === 'user') {
        document.querySelectorAll('input[name="user-assigned-nodes"]:checked').forEach(cb => nodes.push(cb.value));
    }
    
    let permissions = [];
    if (role === 'user') {
        const checkedPerms = document.querySelectorAll('#user-permissions-list input[type="checkbox"]:checked');
        checkedPerms.forEach(cb => permissions.push(cb.value));
    } else {
        permissions = ['start', 'stop', 'restart', 'console', 'config', 'files', 'backup'];
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
                    nodes,
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
                    nodes,
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
        opt.textContent = serverLabel(server);
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
        
        if (el.cloudbackupEnabled) el.cloudbackupEnabled.disabled = true;
        if (el.cloudbackupRemote) el.cloudbackupRemote.disabled = true;
        if (el.cloudbackupPath) el.cloudbackupPath.disabled = true;
        if (el.cloudbackupBtnSave) el.cloudbackupBtnSave.disabled = true;
        if (el.cloudbackupBtnSync) el.cloudbackupBtnSync.disabled = true;
        if (el.cloudbackupMessage) el.cloudbackupMessage.textContent = '';
        
        const placeholder = document.createElement('tr');
        placeholder.innerHTML = `<td colspan="4" class="text-center text-muted" style="padding: 2rem;">${t('backups-no-server-selected')}</td>`;
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

    if (el.cloudbackupEnabled) el.cloudbackupEnabled.disabled = false;
    if (el.cloudbackupRemote) el.cloudbackupRemote.disabled = false;
    if (el.cloudbackupPath) el.cloudbackupPath.disabled = false;
    // Cloud settings are saved by administrators only (the backend requires it).
    if (el.cloudbackupBtnSave) el.cloudbackupBtnSave.disabled = !(state.currentUser && state.currentUser.role === 'admin');

    // Read-only or offline node: viewing and downloading only.
    if (serverWriteLocked(state.selectedBackupServer)) {
        [el.backupsBtnCreate, el.backupsBtnUpload, el.backupsSettingMaxBackups, el.backupsSettingMaxBackupDays,
            el.backupsSettingStopOnBackup, el.backupsSettingsSaveBtn, el.backupsSettingAutoBackupEnabled,
            el.backupsSettingCronPreset, el.backupsSettingCronCustom, el.cloudbackupEnabled, el.cloudbackupRemote,
            el.cloudbackupPath, el.cloudbackupBtnSave].forEach(c => { if (c) c.disabled = true; });
        el.backupsSettingsMessage.textContent = serverWriteLockedText(state.selectedBackupServer);
    }

    loadBackupsList();
    loadBackupSettings();
    loadCloudBackupSettings();
}

async function loadBackupsList() {
    const serverId = state.selectedBackupServer;
    if (!serverId) return;
    
    el.backupsLoading.classList.remove('hidden');
    el.backupsEmptyMessage.classList.add('hidden');
    el.backupsTableBody.innerHTML = '';
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/backups`);
        if (serverId !== state.selectedBackupServer) return; // another server was chosen meanwhile
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
                        ${serverWriteLocked(serverId) ? '' : `
                        <button class="btn btn-warning btn-sm" onclick="confirmRestoreBackup(${jsArg(serverId)}, ${jsArg(backup.name)})" style="display: inline-flex; align-items: center; gap: 0.25rem;">
                            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 2v6h6M2.66 15.57a10 10 0 1 0-.57-8.38l5.67-5.67"></path></svg>
                            ${t('btn-restore')}
                        </button>
                        <button class="btn btn-danger btn-sm" onclick="confirmDeleteBackup(${jsArg(serverId)}, ${jsArg(backup.name)})" style="display: inline-flex; align-items: center; gap: 0.25rem;">
                            <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="3 6 5 6 21 6"></polyline><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"></path></svg>
                            ${t('btn-delete')}
                        </button>`}
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
            if (serverId !== state.selectedBackupServer) return;
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

    if (!['.tar.gz', '.tar.zst', '.tar'].some(ext => file.name.endsWith(ext))) {
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

    xhr.open('POST', `/api/servers/${encodeURIComponent(serverId)}/backups/upload`);
    xhr.setRequestHeader('X-CSRF-Token', getCsrfToken());
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
    runStreamModal({
        title: (state.language === 'de' ? `Backup-Wiederherstellung für {server}` : `Backup Restore for {server}`).replace('{server}', serverId),
        runningText: state.language === 'de' ? 'Wiederherstellung läuft...' : 'Restoring backup...',
        url: `/api/servers/${encodeURIComponent(serverId)}/backups/restore`,
        body: { file: filename, lang: state.language },
        errorText: state.language === 'de' ? 'Fehler bei der Wiederherstellung.' : 'Restore failed.',
        onExit: () => loadBackupsList()
    });
}

function formatBytes(bytes) {
    if (bytes === 0) return '0 Bytes';
    const k = 1024;
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

function escapeHtml(str) {
    return String(str ?? '').replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#039;");
}

// Encodes a value as JavaScript string literal for use inside an inline event handler attribute
// (onclick="fn(${jsArg(value)})"). The browser HTML-decodes attribute values before the JS is
// parsed, so the value must be JSON-encoded first and HTML-escaped second.
function jsArg(value) {
    return escapeHtml(JSON.stringify(String(value ?? '')));
}

function toggleContainer(checkbox, container) {
    if (checkbox.checked) {
        container.classList.remove('hidden');
    } else {
        container.classList.add('hidden');
    }
}

// Alert settings are LinuxGSM variables; the server sends the services with their fields, the
// values the user set (secrets masked), LinuxGSM's defaults and the file each value comes from.
const ALERT_SERVICE_NAMES = {
    discord: 'Discord', email: 'E-Mail', gotify: 'Gotify', ifttt: 'IFTTT', matrix: 'Matrix', ntfy: 'ntfy',
    pushbullet: 'Pushbullet', pushover: 'Pushover', rocketchat: 'Rocket.Chat', slack: 'Slack', telegram: 'Telegram'
};
const ALERT_FIELD_LABELS = {
    statusalert: ['Alert on start, stop and restart', 'Bei Start, Stopp und Neustart benachrichtigen'],
    postalert: ['Attach details (post to termbin.com)', 'Details anhängen (Upload zu termbin.com)'],
    displayip: ['IP address shown in alerts', 'In Benachrichtigungen angezeigte IP-Adresse'],
    discordwebhook: ['Webhook URL', 'Webhook-URL'],
    email: ['Recipient (sent with the local mail command)', 'Empfänger (Versand über den lokalen mail-Befehl)'],
    emailfrom: ['Sender (optional)', 'Absender (optional)'],
    gotifytoken: ['App token', 'App-Token'], gotifywebhook: ['Server URL', 'Server-URL'],
    ifttttoken: ['Webhooks key', 'Webhooks-Key'], iftttevent: ['Event name', 'Event-Name'],
    matrixhomeserver: ['Homeserver', 'Homeserver'], matrixtoken: ['Access token', 'Access-Token'], matrixroom: ['Room ID', 'Raum-ID'],
    ntfyserver: ['Server', 'Server'], ntfytopic: ['Topic', 'Topic'], ntfytoken: ['Access token (optional)', 'Access-Token (optional)'],
    ntfyusername: ['User name (optional)', 'Benutzername (optional)'], ntfypassword: ['Password (optional)', 'Passwort (optional)'],
    ntfypriority: ['Priority (optional)', 'Priorität (optional)'], ntfytags: ['Tags (optional)', 'Tags (optional)'],
    pushbullettoken: ['Access token', 'Access-Token'], channeltag: ['Channel tag (optional)', 'Channel-Tag (optional)'],
    pushovertoken: ['App token', 'App-Token'], pushoveruserkey: ['User key', 'User-Key'],
    rocketchatwebhook: ['Webhook URL', 'Webhook-URL'], slackwebhook: ['Webhook URL', 'Webhook-URL'],
    telegramtoken: ['Bot token', 'Bot-Token'], telegramchatid: ['Chat ID', 'Chat-ID'],
    telegramthreadid: ['Thread ID (optional)', 'Thread-ID (optional)'], telegramapi: ['API host', 'API-Host']
};
let alertSettingsLoadedFor = '';

function alertLabel(key) {
    const l = ALERT_FIELD_LABELS[key];
    return l ? (state.language === 'de' ? l[1] : l[0]) : key;
}

// A value from common.cfg applies to every instance in the directory; the file is shown.
function alertSourceNote(sources, key) {
    const src = sources[key];
    if (!src || !src.includes('common')) return '';
    return ` <small class="text-muted">(${escapeHtml(src)})</small>`;
}

function alertInputHtml(f, values, defaults, sources) {
    const id = `alerts-field-${f.key}`;
    const placeholder = f.secret ? '' : (defaults[f.key] || '');
    return `<label for="${id}" style="font-size:0.85rem; display:block;" class="mt-2">${escapeHtml(alertLabel(f.key))}${alertSourceNote(sources, f.key)}</label>
        <input type="${f.secret ? 'password' : 'text'}" id="${id}" data-alert-key="${escapeHtml(f.key)}" class="form-control mt-1"
            value="${escapeHtml(values[f.key] || '')}" placeholder="${escapeHtml(placeholder)}" autocomplete="off">`;
}

function renderAlertFields(data) {
    const values = data.values || {}, defaults = data.defaults || {}, sources = data.sources || {};
    const isOn = (key) => (values[key] ?? defaults[key]) === 'on';
    let html = '';
    (data.services || []).forEach(svc => {
        const fields = svc.fields || [];
        const general = svc.id === 'general';
        html += `<div class="form-group mb-3" data-alert-service="${escapeHtml(svc.id)}">`;
        if (general) {
            html += `<div style="font-weight:600; margin-bottom:0.25rem;">${state.language === 'de' ? 'Allgemein' : 'General'}</div>`;
            fields.forEach(f => {
                html += f.switch
                    ? `<label style="display:flex; align-items:center; gap:0.5rem; cursor:pointer;"><input type="checkbox" data-alert-key="${escapeHtml(f.key)}" ${isOn(f.key) ? 'checked' : ''}> <span>${escapeHtml(alertLabel(f.key))}</span>${alertSourceNote(sources, f.key)}</label>`
                    : alertInputHtml(f, values, defaults, sources);
            });
        } else if (fields.length) {
            const sw = fields[0];
            const name = ALERT_SERVICE_NAMES[svc.id] || svc.id;
            html += `<label style="display:flex; align-items:center; gap:0.5rem; font-weight:600; cursor:pointer;"><input type="checkbox" data-alert-key="${escapeHtml(sw.key)}" ${isOn(sw.key) ? 'checked' : ''}> <span>${escapeHtml(name)}${state.language === 'de' ? '-Alerts' : ' alerts'}</span>${alertSourceNote(sources, sw.key)}</label>`;
            html += `<div class="mt-2${isOn(sw.key) ? '' : ' hidden'}" data-alert-details>`;
            fields.slice(1).forEach(f => { html += alertInputHtml(f, values, defaults, sources); });
            html += '</div>';
        }
        html += '</div>';
    });
    el.alertsFields.innerHTML = html;
    el.alertsFields.querySelectorAll('[data-alert-service]').forEach(group => {
        const sw = group.querySelector('input[type=checkbox]');
        const details = group.querySelector('[data-alert-details]');
        if (sw && details) sw.addEventListener('change', () => details.classList.toggle('hidden', !sw.checked));
    });
}

async function loadAlertSettings() {
    const serverId = el.settingsServerSelect.value;
    if (!serverId) return;
    alertSettingsLoadedFor = '';
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(serverId)}/alerts`);
        if (res.status !== 200 || el.settingsServerSelect.value !== serverId) return;
        renderAlertFields(await res.json());
        alertSettingsLoadedFor = serverId;
        setAlertFieldsDisabled(serverWriteLocked(serverId));
    } catch (err) {
        console.error('Error loading alert settings:', err);
    }
}

async function saveAlertSettings(e) {
    if (e) e.preventDefault();
    const serverId = el.settingsServerSelect.value;
    // Never save a form that was loaded for another server or did not load at all.
    if (!serverId || alertSettingsLoadedFor !== serverId) return;

    el.alertsSettingsMessage.textContent = state.language === 'de' ? 'Wird gespeichert...' : 'Saving...';
    el.alertsSettingsMessage.className = 'info-message text-muted';
    el.alertsSettingsSaveBtn.disabled = true;
    const values = {};
    el.alertsFields.querySelectorAll('[data-alert-key]').forEach(input => {
        values[input.dataset.alertKey] = input.type === 'checkbox' ? (input.checked ? 'on' : 'off') : input.value.trim();
    });
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(serverId)}/alerts`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ values })
        });
        el.alertsSettingsSaveBtn.disabled = false;
        if (res.status === 200) {
            el.alertsSettingsMessage.textContent = state.language === 'de' ? 'Benachrichtigungs-Einstellungen erfolgreich gespeichert.' : 'Alert settings saved successfully.';
            el.alertsSettingsMessage.className = 'info-message text-success';
            setTimeout(() => {
                if (el.settingsServerSelect.value === serverId) el.alertsSettingsMessage.textContent = '';
            }, 3000);
            loadAlertSettings();
        } else {
            const data = await res.json().catch(() => ({}));
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
    alertSettingsLoadedFor = '';
    if (el.alertsFields) el.alertsFields.innerHTML = '';
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

// Game specific actions: only the commands LinuxGSM offers for this game (server.commands, see
// backend/commands.go). The TS3 password change stays hidden: LinuxGSM's command does not apply
// the password.
const GAME_ACTIONS = [
    { cmd: 'map-wipe', cls: 'btn-danger', label: ['Map wipe', 'Map-Wipe'], confirm: ['Perform a map wipe? All player structures will be deleted!', 'Map-Wipe durchführen? Alle Bauwerke werden gelöscht!'] },
    { cmd: 'full-wipe', cls: 'btn-danger', label: ['Full wipe', 'Full-Wipe'], confirm: ['Perform a full wipe? All map progress and blueprints will be deleted!', 'Full-Wipe durchführen? Alle Bauwerke und Blueprints werden gelöscht!'] },
    { cmd: 'map-compressor', cls: 'btn-secondary', label: ['Compress maps', 'Maps komprimieren'], confirm: ['Run the map compressor?', 'Map-Komprimierung ausführen?'] },
    { cmd: 'mods-install', cls: 'btn-primary', label: ['Mods and plugins', 'Mods und Plugins'], open: 'mods' },
    { cmd: 'mods-update', cls: 'btn-secondary', label: ['Update mods', 'Mods aktualisieren'] },
    { cmd: 'workshop-update', cls: 'btn-secondary', label: ['Update Workshop mods', 'Workshop-Mods aktualisieren'] },
    { cmd: 'install-default-resources', cls: 'btn-primary', label: ['Install MTA default resources', 'MTA-Standardressourcen installieren'] },
    { cmd: 'fastdl', cls: 'btn-primary', label: ['Build FastDL (bzip2)', 'FastDL erzeugen (bzip2)'], fastdlPanel: true }
];

function serverHasCommand(server, cmd) {
    return Array.isArray(server.commands) && server.commands.includes(cmd);
}

function renderConsoleGameActions(server) {
    const hide = () => {
        if (!el.consoleGameActions) return;
        el.consoleGameActions.classList.add('hidden');
        el.consoleGameActions.innerHTML = '';
    };
    const isAdmin = state.currentUser && state.currentUser.role === 'admin';
    if (!server || !el.consoleGameActions || !isAdmin) {
        hide();
        return;
    }
    const isDe = state.language === 'de';
    const disabledAttr = (server.status === 'installing' || server.status === 'updating') ? 'disabled' : '';
    const actions = GAME_ACTIONS.filter(a => serverHasCommand(server, a.cmd));
    if (actions.length === 0) {
        hide();
        return;
    }

    let html = '';
    actions.forEach(a => {
        html += `<button id="game-act-${a.cmd}" class="btn ${a.cls} btn-sm" ${disabledAttr}><span>${escapeHtml(isDe ? a.label[1] : a.label[0])}</span></button>`;
    });
    if (actions.some(a => a.fastdlPanel)) {
        html += `<div id="game-fastdl-info" data-server="${escapeHtml(server.id)}" style="width: 100%; margin-top: 0.5rem; background: rgba(0,0,0,0.3); border: 1px solid var(--border-color); border-radius: 6px; padding: 0.5rem 0.75rem; display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; flex-wrap: wrap;"></div>`;
    }
    el.consoleGameActions.innerHTML = html;
    el.consoleGameActions.classList.remove('hidden');
    if (actions.some(a => a.fastdlPanel)) loadFastDLInfo(server);

    actions.forEach(a => {
        const btn = document.getElementById(`game-act-${a.cmd}`);
        if (!btn) return;
        btn.addEventListener('click', () => {
            if (a.open === 'mods') {
                openModsModal(server);
                return;
            }
            if (a.confirm && !confirm(isDe ? a.confirm[1] : a.confirm[0])) return;
            runServerAction(server.id, a.cmd);
        });
    });
}

function updateBulkToolbarState() {
    const checkboxes = document.querySelectorAll('.bulk-server-checkbox:checked');
    const hasChecked = checkboxes.length > 0;
    
    if (el.btnBulkStart) el.btnBulkStart.disabled = !hasChecked;
    if (el.btnBulkStop) el.btnBulkStop.disabled = !hasChecked;
    if (el.btnBulkRestart) el.btnBulkRestart.disabled = !hasChecked;
    if (el.btnBulkUpdate) el.btnBulkUpdate.disabled = !hasChecked;
}

async function runBulkAction(action) {
    const checked = Array.from(document.querySelectorAll('.bulk-server-checkbox:checked')).map(cb => cb.dataset.id);
    if (checked.length === 0) return;
    
    const confirmMsg = state.language === 'de' ? 
        `Möchtest du die Aktion "${action}" für ${checked.length} ausgewählte Server ausführen?` :
        `Execute bulk action "${action}" for ${checked.length} selected server(s)?`;

    if (!confirm(confirmMsg)) return;
    
    try {
        const res = await apiFetch('/api/servers/bulk', {
            method: 'POST',
            body: JSON.stringify({ action: action, serverIds: checked })
        });
        const data = await res.json().catch(() => ({}));
        if (res.status !== 200) {
            alert((state.language === 'de' ? 'Sammelaktion fehlgeschlagen: ' : 'Bulk action failed: ') + (data.error || res.status));
            return;
        }
        // Report every server that did not simply succeed (offline or read-only node, warnings, errors).
        const problems = Object.entries(data).filter(([, result]) => result !== 'Success');
        if (problems.length > 0) {
            const name = (id) => { const s = state.servers.find(x => x.id === id); return s ? serverLabel(s) : id; };
            alert((state.language === 'de' ? 'Ergebnis der Sammelaktion:\n' : 'Bulk action result:\n') +
                problems.map(([id, result]) => `${name(id)}: ${result}`).join('\n'));
        }
        refreshDashboard();
    } catch (e) {
        console.error('Bulk action error:', e);
        alert((state.language === 'de' ? 'Sammelaktion fehlgeschlagen: ' : 'Bulk action failed: ') + e.message);
    }
}

function openPlayersModal(serverId) {
    const server = state.servers.find(s => s.id === serverId);
    if (!server || !el.modalPlayers) return;
    const isDe = state.language === 'de';

    el.modalPlayersTitle.textContent = `${server.name} - ${isDe ? 'Spieler' : 'Players'}`;

    const query = server.query_data || {};
    const updated = query.updated ? new Date(query.updated).toLocaleTimeString() : '-';
    el.modalPlayersInfo.innerHTML = `
        <span><strong>${isDe ? 'Karte' : 'Map'}:</strong> ${escapeHtml(query.map || '-')}</span>
        <span><strong>${isDe ? 'Spieler' : 'Players'}:</strong> ${escapeHtml(query.num_players || 0)} / ${escapeHtml(query.max_players || 0)}${query.bots ? ` (${escapeHtml(query.bots)} Bots)` : ''}</span>
        <span><strong>Ping:</strong> ${escapeHtml(query.ping || 0)} ms</span>
        <span class="text-muted">${isDe ? 'Stand' : 'Updated'}: ${escapeHtml(updated)}</span>`;

    el.modalPlayersTbody.innerHTML = '';
    const players = query.players || [];
    const canKick = hasUserPermission('console') && !isReadOnlyNodeServer(server) && !server.node_offline;

    if (players.length === 0) {
        const msg = query.num_players > 0
            ? (isDe ? 'Der Server liefert keine Spielernamen.' : 'The server does not report player names.')
            : (isDe ? 'Keine Spieler online.' : 'No players online.');
        el.modalPlayersTbody.innerHTML = `<tr><td colspan="4" class="text-center text-muted" style="padding:1.5rem;">${msg}</td></tr>`;
    } else {
        players.forEach(p => {
            const tr = document.createElement('tr');
            tr.innerHTML = `
                <td><strong>${escapeHtml(p.name)}</strong></td>
                <td>${escapeHtml(p.score || 0)}</td>
                <td>${escapeHtml(p.time || '-')}</td>
                <td class="text-right">${canKick ? `
                    <button class="btn btn-secondary btn-xs" onclick="sendRconCommand(${jsArg(server.id)}, ${jsArg('kick ' + p.name)})">Kick</button>
                    <button class="btn btn-danger btn-xs" onclick="sendRconCommand(${jsArg(server.id)}, ${jsArg('ban ' + p.name)})">Ban</button>` : ''}
                </td>
            `;
            el.modalPlayersTbody.appendChild(tr);
        });
    }

    el.modalPlayers.classList.remove('hidden');
}

function closePlayersModal() {
    if (el.modalPlayers) el.modalPlayers.classList.add('hidden');
}

async function sendRconCommand(serverId, command) {
    const promptMsg = state.language === 'de' ? `RCON-Befehl ausführen: ${command}?` : `Execute RCON command: ${command}?`;
    if (!confirm(promptMsg)) return;
    try {
        const res = await apiFetch(`/api/servers/${serverId}/rcon`, {
            method: 'POST',
            body: JSON.stringify({ command: command })
        });
        if (res.status === 200) {
            alert(state.language === 'de' ? 'Befehl erfolgreich gesendet!' : 'Command sent successfully!');
        }
    } catch (e) {
        console.error('RCON error:', e);
    }
}

// Scheduler: one cron expression per LinuxGSM command. Presets fill the expression; anything
// else stays as "custom", so the form never changes a job it cannot show.
const CRON_JOB_PRESETS = {
    'monitor': [['*/5 * * * *', 'Every 5 minutes', 'Alle 5 Minuten'], ['*/10 * * * *', 'Every 10 minutes', 'Alle 10 Minuten'], ['*/15 * * * *', 'Every 15 minutes', 'Alle 15 Minuten']],
    'update': [['*/30 * * * *', 'Every 30 minutes', 'Alle 30 Minuten'], ['0 * * * *', 'Every hour', 'Jede Stunde'], ['0 */6 * * *', 'Every 6 hours', 'Alle 6 Stunden']],
    'force-update': [['30 4 * * *', 'Daily at 04:30', 'Täglich um 04:30'], ['0 5 * * *', 'Daily at 05:00', 'Täglich um 05:00']],
    'restart': [['30 4 * * *', 'Daily at 04:30', 'Täglich um 04:30'], ['0 6 * * *', 'Daily at 06:00', 'Täglich um 06:00']],
    'update-lgsm': [['0 0 * * 0', 'Sundays at 00:00', 'Sonntags um 00:00'], ['0 0 * * 1', 'Mondays at 00:00', 'Montags um 00:00']]
};
const CRON_JOB_LABELS = {
    'monitor': ['Crash monitor (monitor)', 'Absturz-Überwachung (monitor)'],
    'update': ['Update check (update)', 'Update-Prüfung (update)'],
    'force-update': ['Forced update and restart (force-update)', 'Erzwungenes Update mit Neustart (force-update)'],
    'restart': ['Restart (restart)', 'Neustart (restart)'],
    'update-lgsm': ['LinuxGSM update (update-lgsm)', 'LinuxGSM-Update (update-lgsm)']
};
let cronLoaded = { serverId: '', jobs: {} };

function renderCronJobs(data) {
    const de = state.language === 'de';
    const jobs = data.jobs || {};
    let html = '';
    (data.available || []).forEach(cmd => {
        const expr = jobs[cmd] || '';
        const presets = CRON_JOB_PRESETS[cmd] || [];
        const isPreset = presets.some(p => p[0] === expr);
        const options = [`<option value="" ${expr === '' ? 'selected' : ''}>${de ? 'Deaktiviert' : 'Disabled'}</option>`]
            .concat(presets.map(p => `<option value="${escapeHtml(p[0])}" ${p[0] === expr ? 'selected' : ''}>${escapeHtml(de ? p[2] : p[1])}</option>`))
            .concat([`<option value="custom" ${expr !== '' && !isPreset ? 'selected' : ''}>${de ? 'Eigener Cron-Ausdruck' : 'Custom cron expression'}</option>`]);
        const label = CRON_JOB_LABELS[cmd] ? CRON_JOB_LABELS[cmd][de ? 1 : 0] : cmd;
        html += `<div data-cron-job="${escapeHtml(cmd)}">
            <label style="font-size:0.85rem; font-weight:600;">${escapeHtml(label)}</label>
            <select class="form-control mt-1" data-cron-select>${options.join('')}</select>
            <input type="text" class="form-control mt-1${expr !== '' && !isPreset ? '' : ' hidden'}" data-cron-expr value="${escapeHtml(expr)}" placeholder="m h dom mon dow" autocomplete="off">
        </div>`;
    });
    el.cronJobs.innerHTML = html;
    el.cronJobs.querySelectorAll('[data-cron-job]').forEach(row => {
        const select = row.querySelector('[data-cron-select]');
        const input = row.querySelector('[data-cron-expr]');
        select.addEventListener('change', () => {
            input.classList.toggle('hidden', select.value !== 'custom');
            if (select.value !== 'custom') input.value = select.value;
        });
    });
}

async function loadCronSchedule(serverId) {
    if (!el.cronJobs) return;
    cronLoaded = { serverId: '', jobs: {} };
    el.cronJobs.innerHTML = '';
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(serverId)}/cron`);
        if (res.status !== 200 || el.settingsServerSelect.value !== serverId) return;
        const data = await res.json();
        renderCronJobs(data);
        cronLoaded = { serverId, jobs: data.jobs || {} };
    } catch (e) {
        console.error('Failed to load cron schedule:', e);
    }
}

async function handleCronBuilderSubmit(e) {
    e.preventDefault();
    const serverId = el.settingsServerSelect.value;
    // Never save a schedule that was loaded for another server or did not load.
    if (!serverId || cronLoaded.serverId !== serverId) return;

    // Only the jobs the user changed are sent; the server leaves all others as they are.
    const jobs = {};
    el.cronJobs.querySelectorAll('[data-cron-job]').forEach(row => {
        const cmd = row.dataset.cronJob;
        const select = row.querySelector('[data-cron-select]');
        const expr = (select.value === 'custom' ? row.querySelector('[data-cron-expr]').value : select.value).trim();
        if (expr !== (cronLoaded.jobs[cmd] || '')) jobs[cmd] = expr;
    });
    if (Object.keys(jobs).length === 0) {
        el.cronBuilderMessage.className = 'info-message text-muted';
        el.cronBuilderMessage.textContent = t('saved');
        return;
    }

    el.cronBuilderMessage.textContent = t('saving');
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(serverId)}/cron`, {
            method: 'POST',
            body: JSON.stringify({ jobs })
        });
        if (res.status === 200) {
            el.cronBuilderMessage.className = 'info-message text-success';
            el.cronBuilderMessage.textContent = t('saved');
            loadCronSchedule(serverId);
        } else {
            const data = await res.json().catch(() => ({}));
            el.cronBuilderMessage.className = 'info-message text-danger';
            el.cronBuilderMessage.textContent = data.error || t('status-error');
        }
    } catch (err) {
        el.cronBuilderMessage.className = 'info-message text-danger';
        el.cronBuilderMessage.textContent = err.message;
    }
}

// ----------------------------------------------------
// Console Command History & Macros
// ----------------------------------------------------
let consoleHistory = [];
let consoleHistoryIndex = -1;

function initConsoleMacrosAndHistory() {
    if (el.consoleInputField) {
        el.consoleInputField.addEventListener('keydown', (e) => {
            if (e.key === 'ArrowUp') {
                if (consoleHistory.length > 0 && consoleHistoryIndex < consoleHistory.length - 1) {
                    consoleHistoryIndex++;
                    el.consoleInputField.value = consoleHistory[consoleHistory.length - 1 - consoleHistoryIndex];
                }
                e.preventDefault();
            } else if (e.key === 'ArrowDown') {
                if (consoleHistoryIndex > 0) {
                    consoleHistoryIndex--;
                    el.consoleInputField.value = consoleHistory[consoleHistory.length - 1 - consoleHistoryIndex];
                } else if (consoleHistoryIndex === 0) {
                    consoleHistoryIndex = -1;
                    el.consoleInputField.value = '';
                }
                e.preventDefault();
            }
        });
    }

    document.querySelectorAll('.console-macro-btn').forEach(btn => {
        btn.addEventListener('click', () => {
            const cmd = btn.getAttribute('data-cmd');
            if (cmd && el.consoleInputField) {
                el.consoleInputField.value = cmd;
                el.consoleInputField.focus();
            }
        });
    });
}

// Push to console history upon submit
if (el.consoleInputForm) {
    el.consoleInputForm.addEventListener('submit', () => {
        const val = el.consoleInputField.value.trim();
        if (val) {
            consoleHistory.push(val);
            if (consoleHistory.length > 50) consoleHistory.shift();
            consoleHistoryIndex = -1;
        }
    });
}

// ----------------------------------------------------
// Mods & Plugins Modal
// ----------------------------------------------------
// mods-install and mods-remove ask LinuxGSM for a mod name, so the mod is picked here: the
// installed ones (to remove) and the ones LinuxGSM offers for the game (to install).
function openModsModal(server) {
    if (!el.modalMods) return;
    const serverId = server.id;
    el.modalMods.dataset.server = serverId;
    el.modalMods.classList.remove('hidden');
    el.modalModsList.innerHTML = `<div class="text-center text-muted py-3">${t('mods-loading')}</div>`;

    const base = `/api/servers/${encodeURIComponent(serverId)}`;
    const getJSON = url => fetch(url).then(r => r.ok ? r.json() : Promise.reject(new Error(String(r.status))));
    const current = () => el.modalMods.dataset.server === serverId && !el.modalMods.classList.contains('hidden');
    Promise.all([getJSON(`${base}/mods`), getJSON(`${base}/mods/available`)])
        .then(([inst, avail]) => {
            if (current()) renderModsModal(server, inst.installed || [], avail.available || []);
        })
        .catch(() => {
            if (current()) el.modalModsList.innerHTML = `<div class="alert alert-danger">${t('mods-load-error')}</div>`;
        });
}

function renderModsModal(server, installed, available) {
    const locked = serverWriteLocked(server.id);
    const lockAttr = locked ? `disabled title="${escapeHtml(serverWriteLockedText(server.id))}"` : '';
    const installedSet = new Set(installed);

    let html = `<h4 class="text-sm mb-2">${t('mods-installed')}</h4>`;
    if (installed.length === 0) {
        html += `<div class="alert alert-info mb-3">${t('mods-none-installed')}</div>`;
    } else {
        html += '<ul class="list-group mb-3">';
        installed.forEach(mod => {
            html += `<li class="list-group-item flex-between align-center">
                <span class="mono">${escapeHtml(mod)}</span>
                <button class="btn btn-danger btn-sm" data-mod-remove="${escapeHtml(mod)}" ${lockAttr}>${t('mods-btn-remove')}</button>
            </li>`;
        });
        html += '</ul>';
    }

    html += `<h4 class="text-sm mb-2">${t('mods-available')}</h4>`;
    if (available.length === 0) {
        html += `<div class="alert alert-info">${t('mods-none-available')}</div>`;
    } else {
        html += '<ul class="list-group">';
        available.forEach(mod => {
            const label = installedSet.has(mod.command) ? t('mods-btn-reinstall') : t('mods-btn-install');
            html += `<li class="list-group-item flex-between align-center" style="gap: 0.75rem;">
                <div style="min-width: 0;">
                    <span class="mono">${escapeHtml(mod.command)}</span>
                    <div class="text-muted text-sm" style="overflow-wrap: anywhere;">${escapeHtml(mod.description)}</div>
                </div>
                <button class="btn btn-primary btn-sm" data-mod-install="${escapeHtml(mod.command)}" ${lockAttr}>${label}</button>
            </li>`;
        });
        html += '</ul>';
    }
    el.modalModsList.innerHTML = html;

    el.modalModsList.querySelectorAll('[data-mod-install]').forEach(btn => btn.addEventListener('click', () => {
        el.modalMods.classList.add('hidden');
        runServerAction(server.id, 'mods-install', { mod: btn.dataset.modInstall });
    }));
    el.modalModsList.querySelectorAll('[data-mod-remove]').forEach(btn => btn.addEventListener('click', () => {
        const mod = btn.dataset.modRemove;
        if (!confirm(t('mods-confirm-remove').replace('{mod}', mod))) return;
        el.modalMods.classList.add('hidden');
        runServerAction(server.id, 'mods-remove', { mod });
    }));
}

if (el.btnCloseModalMods) el.btnCloseModalMods.addEventListener('click', () => el.modalMods.classList.add('hidden'));
if (el.btnCloseModsModalFooter) el.btnCloseModsModalFooter.addEventListener('click', () => el.modalMods.classList.add('hidden'));

// ----------------------------------------------------
// TeamSpeak 3 ServerAdmin password
// ----------------------------------------------------
// LinuxGSM writes the password into a shell line, so the backend accepts only these characters.
const TS3_PASSWORD_RE = /^[A-Za-z0-9._+=@#%^*!?:,~-]{8,64}$/;

function openTs3PasswordModal(server) {
    const modal = document.getElementById('modal-ts3pw');
    if (!modal) return;
    modal.dataset.server = server.id;
    document.getElementById('ts3pw-password').value = '';
    document.getElementById('ts3pw-confirm').value = '';
    document.getElementById('ts3pw-error').textContent = '';
    modal.classList.remove('hidden');
    document.getElementById('ts3pw-password').focus();
}

(function initTs3PasswordModal() {
    const modal = document.getElementById('modal-ts3pw');
    const form = document.getElementById('ts3pw-form');
    if (!modal || !form) return;
    const close = () => {
        modal.classList.add('hidden');
        document.getElementById('ts3pw-password').value = '';
        document.getElementById('ts3pw-confirm').value = '';
    };
    document.getElementById('btn-close-modal-ts3pw').addEventListener('click', close);
    document.getElementById('btn-cancel-ts3pw').addEventListener('click', close);
    form.addEventListener('submit', e => {
        e.preventDefault();
        const password = document.getElementById('ts3pw-password').value;
        const errorEl = document.getElementById('ts3pw-error');
        if (!TS3_PASSWORD_RE.test(password)) {
            errorEl.textContent = t('ts3pw-invalid');
            return;
        }
        if (password !== document.getElementById('ts3pw-confirm').value) {
            errorEl.textContent = t('ts3pw-mismatch');
            return;
        }
        const serverId = modal.dataset.server;
        close();
        runServerAction(serverId, 'change-password', { password });
    });
})();

// ----------------------------------------------------
// Web File Manager
// ----------------------------------------------------
let currentFilesServer = '';
let currentFilesSubpath = '';

function initFileManager() {
    if (!el.filesServerSelect) return;

    el.filesServerSelect.addEventListener('change', () => {
        currentFilesServer = el.filesServerSelect.value;
        currentFilesSubpath = '';
        // An open file belongs to the previous server.
        fileEditorSeq++;
        fileEditorLoaded = null;
        el.filesEditorCard.classList.add('hidden');
        applyFilesUploadLock();
        loadFilesTree();
    });

    if (el.filesBtnRefresh) {
        el.filesBtnRefresh.addEventListener('click', () => loadFilesTree());
    }

    if (el.filesBtnCloseEditor) {
        el.filesBtnCloseEditor.addEventListener('click', () => {
            el.filesEditorCard.classList.add('hidden');
        });
    }

    if (el.filesBtnSave) {
        el.filesBtnSave.addEventListener('click', saveFileContent);
    }

    if (el.filesBtnUpload && el.filesInputUpload) {
        el.filesBtnUpload.addEventListener('click', () => {
            if (!currentFilesServer) {
                alert(t('files-select-prompt'));
                return;
            }
            el.filesInputUpload.click();
        });

        el.filesInputUpload.addEventListener('change', async () => {
            const file = el.filesInputUpload.files[0];
            if (!file || !currentFilesServer) return;

            const formData = new FormData();
            formData.append('file', file);
            formData.append('path', currentFilesSubpath);

            try {
                const res = await apiFetch(`/api/servers/${currentFilesServer}/files/upload`, {
                    method: 'POST',
                    body: formData
                });
                if (res.status === 200) {
                    alert(state.language === 'de' ? 'Datei erfolgreich hochgeladen!' : 'File uploaded successfully!');
                    loadFilesTree();
                } else {
                    alert(state.language === 'de' ? 'Fehler beim Hochladen der Datei.' : 'Error uploading file.');
                }
            } catch (e) {
                alert((state.language === 'de' ? 'Fehler: ' : 'Error: ') + e.message);
            } finally {
                el.filesInputUpload.value = '';
            }
        });
    }
}

function updateFilesServerSelect() {
    if (!el.filesServerSelect) return;
    const currentVal = el.filesServerSelect.value;
    el.filesServerSelect.innerHTML = `<option value="">${t('files-select-default')}</option>`;
    state.servers.forEach(srv => {
        const opt = document.createElement('option');
        opt.value = srv.id;
        opt.textContent = `${srv.name} (${srv.id})`;
        el.filesServerSelect.appendChild(opt);
    });
    if (currentVal) el.filesServerSelect.value = currentVal;
}

async function loadFilesTree() {
    if (!currentFilesServer) {
        el.filesTableBody.innerHTML = `<tr><td colspan="4" class="text-muted text-center py-4">${t('files-select-prompt')}</td></tr>`;
        return;
    }

    el.filesBreadcrumb.textContent = `/serverfiles/${currentFilesSubpath}`;
    try {
        const res = await apiFetch(`/api/servers/${currentFilesServer}/files?path=${encodeURIComponent(currentFilesSubpath)}`);
        if (res.status === 200) {
            const files = await res.json();
            renderFilesTable(files);
        } else {
            el.filesTableBody.innerHTML = `<tr><td colspan="4" class="text-danger text-center py-4">Error loading files</td></tr>`;
        }
    } catch (e) {
        console.error('File manager error:', e);
        el.filesTableBody.innerHTML = `<tr><td colspan="4" class="text-danger text-center py-4">${escapeHtml(e.message)}</td></tr>`;
    }
}

function renderFilesTable(files) {
    el.filesTableBody.innerHTML = '';

    if (currentFilesSubpath) {
        const backTr = document.createElement('tr');
        backTr.innerHTML = `
            <td colspan="4" class="font-mono text-sm cursor-pointer hover:bg-black-10 py-2" onclick="navigateFilesUp()">
                <strong>..</strong> <span class="text-muted">(${state.language === 'de' ? 'übergeordneter Ordner' : 'parent directory'})</span>
            </td>
        `;
        el.filesTableBody.appendChild(backTr);
    }

    if (!files || files.length === 0) {
        const tr = document.createElement('tr');
        tr.innerHTML = `<td colspan="4" class="text-muted text-center py-4">Empty Directory</td>`;
        el.filesTableBody.appendChild(tr);
        return;
    }

    files.forEach(f => {
        const tr = document.createElement('tr');
        const icon = f.is_dir ? '<span class="file-kind is-dir">DIR</span>' : '<span class="file-kind">FILE</span>';
        const sizeText = f.is_dir ? '--' : formatBytes(f.size);

        tr.innerHTML = `
            <td>
                <span class="font-mono cursor-pointer" onclick="${f.is_dir ? `openFileDir(${jsArg(f.path)})` : `openFileEditor(${jsArg(f.path)})`}">
                    ${icon} <strong>${escapeHtml(f.name)}</strong>
                </span>
            </td>
            <td class="text-sm text-muted">${sizeText}</td>
            <td class="text-sm text-muted">${escapeHtml(f.mod_time)}</td>
            <td>
                ${f.is_dir ? '' : `
                    <button class="btn btn-secondary btn-xs" onclick="openFileEditor(${jsArg(f.path)})">Edit</button>
                    <a href="/api/servers/${currentFilesServer}/files/download?path=${encodeURIComponent(f.path)}" class="btn btn-secondary btn-xs" download style="text-decoration:none;">Download</a>
                `}
            </td>
        `;
        el.filesTableBody.appendChild(tr);
    });
}

function openFileDir(path) {
    currentFilesSubpath = path;
    loadFilesTree();
}

function navigateFilesUp() {
    const parts = currentFilesSubpath.split('/').filter(Boolean);
    parts.pop();
    currentFilesSubpath = parts.join('/');
    loadFilesTree();
}

let activeEditingFilePath = '';

// applyFilesUploadLock disables uploads for servers of read-only or offline nodes.
function applyFilesUploadLock() {
    if (!el.filesBtnUpload) return;
    const locked = !!currentFilesServer && serverWriteLocked(currentFilesServer);
    el.filesBtnUpload.disabled = locked;
    el.filesBtnUpload.title = locked ? serverWriteLockedText(currentFilesServer) : '';
}

// The editor only saves what it loaded: after a failed load (file too large, error) or for a
// binary file there is nothing to save, and a late answer for a file that is no longer open is
// dropped. Otherwise Save would write the error text or another file's content over the file.
let fileEditorSeq = 0;
let fileEditorLoaded = null; // { server, path } of the content in the textarea

function setFileEditorEditable(editable, lockText) {
    el.filesEditorTextarea.readOnly = !editable;
    if (el.filesBtnSave) {
        el.filesBtnSave.disabled = !editable;
        el.filesBtnSave.title = editable ? '' : (lockText || '');
    }
}

async function openFileEditor(path) {
    if (!currentFilesServer) return;
    const serverId = currentFilesServer;
    const seq = ++fileEditorSeq;
    fileEditorLoaded = null;
    activeEditingFilePath = path;
    el.filesEditingFilename.textContent = path;
    el.filesEditorCard.classList.remove('hidden');
    setFileEditorEditable(false);
    el.filesEditorTextarea.value = state.language === 'de' ? 'Lade Dateiinhalt...' : 'Loading file content...';

    let text = null, error = '';
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(serverId)}/files/content?path=${encodeURIComponent(path)}`);
        if (res.status === 200) {
            text = await res.text();
        } else {
            const data = await res.json().catch(() => ({}));
            error = data.error || '';
        }
    } catch (e) {
        error = e.message;
    }
    if (seq !== fileEditorSeq) return; // another file was opened meanwhile

    if (text === null) {
        el.filesEditorTextarea.value = (state.language === 'de' ? 'Fehler beim Lesen des Dateiinhalts.' : 'Error reading file content.') + (error ? ` (${error})` : '');
        return;
    }
    if (text.includes('\u0000') || (text.match(/�/g) || []).length > 8) {
        el.filesEditorTextarea.value = state.language === 'de'
            ? 'Binärdatei: sie kann im Browser nicht bearbeitet werden. Nutze den Download.'
            : 'Binary file: it cannot be edited in the browser. Use the download instead.';
        return;
    }
    el.filesEditorTextarea.value = text;
    fileEditorLoaded = { server: serverId, path };
    // Files of a read-only or offline node can be read, not saved.
    const locked = serverWriteLocked(serverId);
    setFileEditorEditable(!locked, locked ? serverWriteLockedText(serverId) : '');
}

async function saveFileContent() {
    if (!currentFilesServer || !activeEditingFilePath) return;
    if (!fileEditorLoaded || fileEditorLoaded.server !== currentFilesServer || fileEditorLoaded.path !== activeEditingFilePath) return;
    try {
        const res = await apiFetch(`/api/servers/${currentFilesServer}/files`, {
            method: 'POST',
            body: JSON.stringify({
                path: activeEditingFilePath,
                content: el.filesEditorTextarea.value
            })
        });
        if (res.status === 200) {
            alert(state.language === 'de' ? 'Datei erfolgreich gespeichert!' : 'File saved successfully!');
        } else {
            alert(state.language === 'de' ? 'Fehler beim Speichern der Datei.' : 'Error saving file.');
        }
    } catch (e) {
        alert((state.language === 'de' ? 'Fehler: ' : 'Error: ') + e.message);
    }
}

// ----------------------------------------------------
// Security Audit Log
// ----------------------------------------------------
async function loadAuditLogs() {
    if (!el.auditTableBody) return;
    el.auditTableBody.innerHTML = `<tr><td colspan="6" class="text-center text-muted py-4">${state.language === 'de' ? 'Lade Audit-Logs...' : 'Loading audit logs...'}</td></tr>`;

    try {
        const res = await apiFetch('/api/system/audit');
        if (res.status === 200) {
            const logs = await res.json();
            renderAuditTable(logs);
        } else {
            el.auditTableBody.innerHTML = `<tr><td colspan="6" class="text-danger text-center py-4">${state.language === 'de' ? 'Fehler beim Laden der Audit-Logs' : 'Failed to load audit logs'}</td></tr>`;
        }
    } catch (e) {
        el.auditTableBody.innerHTML = `<tr><td colspan="6" class="text-danger text-center py-4">${escapeHtml(e.message)}</td></tr>`;
    }
}

function renderAuditTable(logs) {
    el.auditTableBody.innerHTML = '';
    if (!logs || logs.length === 0) {
        el.auditTableBody.innerHTML = `<tr><td colspan="6" class="text-muted text-center py-4">${t('audit-empty')}</td></tr>`;
        return;
    }

    logs.forEach(log => {
        const tr = document.createElement('tr');
        tr.innerHTML = `
            <td class="font-mono text-sm">${escapeHtml(log.timestamp)}</td>
            <td><strong>${escapeHtml(log.username)}</strong></td>
            <td class="font-mono text-sm">${escapeHtml(log.ip)}</td>
            <td><span class="badge badge-primary">${escapeHtml(log.action)}</span></td>
            <td class="text-sm">${escapeHtml(log.details)}</td>
            <td class="font-mono text-sm">${escapeHtml(log.server_id || '--')}</td>
        `;
        el.auditTableBody.appendChild(tr);
    });
}

if (el.auditBtnRefresh) el.auditBtnRefresh.addEventListener('click', loadAuditLogs);

async function editServerTag(serverId, currentTag) {
    const promptText = t('tag-prompt');
    const newTag = prompt(promptText, currentTag || '');
    if (newTag === null) return;
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/tag`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ tag: newTag.trim() })
        });
        if (res.status === 200) {
            const server = state.servers.find(s => s.id === serverId);
            if (server) server.tag = newTag.trim();
            renderServersGrid();
            if (el.settingsServerSelect && el.settingsServerSelect.value === serverId && el.settingsServerTagInput) {
                el.settingsServerTagInput.value = newTag.trim();
            }
        } else {
            const data = await res.json();
            alert(t('tag-save-error') + ' ' + (data.error || 'Unknown error'));
        }
    } catch (e) {
        console.error('Error setting tag:', e);
        alert(t('tag-save-error') + ' ' + e.message);
    }
}

async function saveServerTagFromSettings() {
    const serverId = el.settingsServerSelect ? el.settingsServerSelect.value : '';
    if (!serverId) return;
    const tagVal = el.settingsServerTagInput ? el.settingsServerTagInput.value.trim() : '';
    
    try {
        const res = await apiFetch(`/api/servers/${serverId}/tag`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ tag: tagVal })
        });
        if (res.status === 200) {
            const server = state.servers.find(s => s.id === serverId);
            if (server) server.tag = tagVal;
            renderServersGrid();
            alert(t('tag-save-success'));
        } else {
            const data = await res.json();
            alert(t('tag-save-error') + ' ' + (data.error || 'Unknown error'));
        }
    } catch (e) {
        alert(t('tag-save-error') + ' ' + e.message);
    }
}

// ----------------------------------------------------
// Stop Mode Configurator
// ----------------------------------------------------
async function loadServerStopMode(serverId) {
    if (!el.settingsStopmodeSelect) return;
    try {
        const res = await apiFetch(`/api/servers/${serverId}/stopmode`);
        if (res.status === 200) {
            const data = await res.json();
            if (el.settingsServerSelect.value !== serverId) return;
            el.settingsStopmodeSelect.value = data.stopmode || 'default';
        }
    } catch (e) {
        console.error('Failed to load stopmode:', e);
    }
}

async function saveServerStopModeFromSettings() {
    const serverId = el.settingsServerSelect ? el.settingsServerSelect.value : '';
    if (!serverId || !el.settingsStopmodeSelect) return;
    const stopMode = el.settingsStopmodeSelect.value;
    try {
        const res = await apiFetch(`/api/servers/${serverId}/stopmode`, {
            method: 'POST',
            body: JSON.stringify({ stopmode: stopMode })
        });
        if (res.status === 200) {
            alert(t('stopmode-save-success'));
        } else {
            alert(t('stopmode-save-error'));
        }
    } catch (e) {
        alert(t('stopmode-save-error') + ': ' + e.message);
    }
}

// ----------------------------------------------------
// Multi-Instance Cloning Assistant
// ----------------------------------------------------
let currentCloneServer = null;

function openCloneModal(server) {
    if (!state.currentUser || state.currentUser.role !== 'admin') {
        alert(state.language === 'de' ? 'Zugriff verweigert: Nur Administratoren dürfen Instanzen klonen.' : 'Access denied: Only administrators can clone instances.');
        return;
    }
    if (!el.modalCloneInstance) return;
    currentCloneServer = server;
    if (el.modalCloneSubtitle) el.modalCloneSubtitle.textContent = `${server.name} (${server.id})`;
    if (el.cloneInstancePrefix) {
        const rootScript = server.script.split('-')[0];
        el.cloneInstancePrefix.textContent = `${rootScript}-`;
    }
    if (el.cloneInstanceSuffix) el.cloneInstanceSuffix.value = '2';
    if (el.modalCloneMessage) el.modalCloneMessage.textContent = '';
    el.modalCloneInstance.classList.remove('hidden');
}

async function handleCloneSubmit() {
    if (!currentCloneServer || !el.cloneInstanceSuffix) return;
    const suffix = el.cloneInstanceSuffix.value.trim().toLowerCase();
    if (!suffix) return;
    
    if (!state.currentUser || state.currentUser.role !== 'admin') {
        if (el.modalCloneMessage) {
            el.modalCloneMessage.className = 'info-message text-danger';
            el.modalCloneMessage.textContent = state.language === 'de' ? 'Zugriff verweigert: Nur Administratoren dürfen Instanzen klonen.' : 'Access denied: Only administrators can clone instances.';
        }
        return;
    }

    if (el.modalCloneMessage) {
        el.modalCloneMessage.className = 'info-message text-muted';
        el.modalCloneMessage.textContent = state.language === 'de' ? 'Erstelle Instanz...' : 'Creating instance...';
    }

    try {
        const res = await apiFetch(`/api/servers/${currentCloneServer.id}/clone`, {
            method: 'POST',
            body: JSON.stringify({ suffix })
        });
        
        if (res.status === 403) {
            if (el.modalCloneMessage) {
                el.modalCloneMessage.className = 'info-message text-danger';
                el.modalCloneMessage.textContent = state.language === 'de' ? 'Zugriff verweigert: Nur Administratoren dürfen Instanzen klonen.' : 'Access denied: Only administrators can clone instances.';
            }
            return;
        }

        let data = {};
        try {
            data = await res.json();
        } catch (err) {}

        if (res.status === 200) {
            if (el.modalCloneMessage) {
                el.modalCloneMessage.className = 'info-message text-success';
                el.modalCloneMessage.textContent = t('clone-success') + ' ' + t('clone-ports-hint');
            }
            setTimeout(() => {
                el.modalCloneInstance.classList.add('hidden');
                refreshDashboard();
            }, 6000);
        } else {
            if (el.modalCloneMessage) {
                el.modalCloneMessage.className = 'info-message text-danger';
                el.modalCloneMessage.textContent = (data.error || t('clone-error'));
            }
        }
    } catch (e) {
        if (el.modalCloneMessage) {
            el.modalCloneMessage.className = 'info-message text-danger';
            el.modalCloneMessage.textContent = t('clone-error') + ': ' + e.message;
        }
    }
}

// ----------------------------------------------------
// Rust Server Wipe Modal
// ----------------------------------------------------
let currentWipeServer = null;

function openRustWipeModal(server) {
    if (!el.modalRustWipe) return;
    currentWipeServer = server;
    if (el.modalRustWipeSubtitle) el.modalRustWipeSubtitle.textContent = `${server.name} (${server.id})`;
    el.modalRustWipe.classList.remove('hidden');
}

function handleRustWipeSubmit() {
    if (!currentWipeServer) return;
    const selectedRadio = document.querySelector('input[name="rust-wipe-type"]:checked');
    const wipeType = selectedRadio ? selectedRadio.value : 'map-wipe';
    el.modalRustWipe.classList.add('hidden');
    runServerAction(currentWipeServer.id, wipeType);
}

// ----------------------------------------------------
// Cloud Backup (rclone) Integration
// ----------------------------------------------------
async function loadCloudBackupSettings() {
    const serverId = state.selectedBackupServer;
    if (!serverId) return;
    try {
        const res = await apiFetch(`/api/servers/${encodeURIComponent(serverId)}/cloudbackup/settings`);
        if (res.status === 200) {
            const data = await res.json();
            if (serverId !== state.selectedBackupServer) return;
            if (el.cloudbackupEnabled) el.cloudbackupEnabled.checked = !!data.enabled;
            if (el.cloudbackupRemote) el.cloudbackupRemote.value = data.remote || '';
            if (el.cloudbackupPath) el.cloudbackupPath.value = data.path || 'backups';
            // Syncing is a write on the node; saving the settings needs an administrator.
            if (el.cloudbackupBtnSync) el.cloudbackupBtnSync.disabled = !data.enabled || !data.remote || serverWriteLocked(serverId);
            if (el.cloudbackupBtnSave) el.cloudbackupBtnSave.disabled = !(state.currentUser && state.currentUser.role === 'admin') || serverWriteLocked(serverId);
        }
    } catch (e) {
        console.error('Failed to load cloud backup settings:', e);
    }
}

async function handleCloudBackupSave(e) {
    e.preventDefault();
    if (!state.selectedBackupServer) return;
    const settings = {
        enabled: el.cloudbackupEnabled ? el.cloudbackupEnabled.checked : false,
        remote: el.cloudbackupRemote ? el.cloudbackupRemote.value.trim() : '',
        path: el.cloudbackupPath ? el.cloudbackupPath.value.trim() : 'backups',
        auto_sync: false
    };

    try {
        const res = await apiFetch(`/api/servers/${state.selectedBackupServer}/cloudbackup/settings`, {
            method: 'POST',
            body: JSON.stringify(settings)
        });
        if (res.status === 200) {
            if (el.cloudbackupMessage) {
                el.cloudbackupMessage.className = 'info-message text-success';
                el.cloudbackupMessage.textContent = t('cloudbackup-save-success');
                setTimeout(() => { if (el.cloudbackupMessage) el.cloudbackupMessage.textContent = ''; }, 3000);
            }
            if (el.cloudbackupBtnSync) el.cloudbackupBtnSync.disabled = !settings.enabled || !settings.remote;
        } else {
            if (el.cloudbackupMessage) {
                el.cloudbackupMessage.className = 'info-message text-danger';
                el.cloudbackupMessage.textContent = t('cloudbackup-save-error');
            }
        }
    } catch (e) {
        if (el.cloudbackupMessage) {
            el.cloudbackupMessage.className = 'info-message text-danger';
            el.cloudbackupMessage.textContent = t('cloudbackup-save-error') + ': ' + e.message;
        }
    }
}

function handleCloudBackupSync() {
    if (!state.selectedBackupServer) return;
    runStreamModal({
        title: `rclone sync - ${state.selectedBackupServer}`,
        runningText: t('status-running-action'),
        url: `/api/servers/${encodeURIComponent(state.selectedBackupServer)}/cloudbackup/sync`,
        body: { lang: state.language },
        errorText: t('action-failed')
    });
}

window.openCloneModal = openCloneModal;
window.openRustWipeModal = openRustWipeModal;

// Initialize Macros and File Manager
initConsoleMacrosAndHistory();
initFileManager();
