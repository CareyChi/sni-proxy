const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const state = { status: null, platform: null, update: null, privileged: false };
const titles = { overview: "运行总览", service: "服务管理", network: "端口与网络", credentials: "管理员凭据", updates: "版本更新", system: "系统信息" };

async function api(path, options = {}) {
  const init = { credentials: "same-origin", ...options };
  if (init.body !== undefined) {
    init.headers = { "Content-Type": "application/json", ...(init.headers || {}) };
    if (typeof init.body !== "string") init.body = JSON.stringify(init.body);
  }
  const response = await fetch(path, init);
  const payload = await response.json().catch(() => ({ error: { message: `HTTP ${response.status}` } }));
  if (!response.ok) {
    if (response.status === 401 && path !== "/api/v1/auth/session") showLogin();
    throw new Error(payload.error?.message || `请求失败 (${response.status})`);
  }
  return payload.data;
}

function showLogin() { $("#app").classList.add("hidden"); $("#login-shell").classList.remove("hidden"); }
function showApp() { $("#login-shell").classList.add("hidden"); $("#app").classList.remove("hidden"); }

let toastTimer;
function toast(message, error = false) {
  const element = $("#toast");
  element.textContent = message;
  element.classList.toggle("error", error);
  element.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => element.classList.remove("show"), 3600);
}

function setStateText(element, value, known = true) {
  element.textContent = known ? (value ? "● 运行中" : "○ 已停止") : "— 未知";
  element.classList.toggle("good", known && value);
  element.classList.toggle("bad", known && !value);
}

async function refreshAll() {
  const [status, system, credentials, ports] = await Promise.all([
    api("/api/v1/status"), api("/api/v1/system/info"), api("/api/v1/admin/credentials"), api("/api/v1/config/ports")
  ]);
  state.status = status;
  state.platform = system.platform;
  state.privileged = status.privileged_operations === true;
  setStateText($("#core-status"), status.core_running, status.core_status_known);
  setStateText($("#web-status"), status.web_running, true);
  $("#autostart-status").textContent = status.autostart_status_known ? (status.autostart_enabled ? "● 已开启" : "○ 已关闭") : "— 未知";
  $("#autostart-status").classList.toggle("good", status.autostart_status_known && status.autostart_enabled);
  $("#install-dir").textContent = status.install_dir;
  $("#hero-version").textContent = status.version;
  $("#update-current").textContent = status.version;
  $("#core-detail").textContent = status.core_status_known ? `状态来自 ${status.init_system}` : "Service Manager 不可用";
  $("#init-summary").textContent = `${system.platform.distribution} / ${system.platform.init_system}`;
  $("#overview-http").textContent = ports.http_port;
  $("#overview-https").textContent = ports.https_port;
  $("#overview-web").textContent = ports.web_port;
  $("#overview-distro").textContent = `${system.platform.distribution} ${system.platform.distribution_version}`;
  $("#overview-init").textContent = system.platform.init_system;
  $("#overview-arch").textContent = system.platform.architecture;
  const form = $("#ports-form");
  form.elements.http_port.value = ports.http_port;
  form.elements.https_port.value = ports.https_port;
  form.elements.web_port.value = ports.web_port;
  $$('[data-service]').forEach((button) => { button.disabled = !state.privileged; });
  form.querySelector('button[type="submit"]').disabled = !state.privileged;
  $("#credentials-form").elements.username.value = credentials.username;
  $$('[data-fact]').forEach((element) => { element.textContent = system.platform[element.dataset.fact] ?? "—"; });
}

$("#login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  $("#login-message").textContent = "";
  try {
    await api("/api/v1/auth/login", { method: "POST", body: { username: form.elements.username.value, password: form.elements.password.value } });
    form.elements.password.value = "";
    showApp();
    await refreshAll();
  } catch (error) { $("#login-message").textContent = error.message; }
});

$("#logout-button").addEventListener("click", async () => {
  try { await api("/api/v1/auth/logout", { method: "POST", body: {} }); } finally { showLogin(); }
});

$$('[data-section]').forEach((button) => button.addEventListener("click", () => {
  $$('[data-section]').forEach((item) => item.classList.toggle("active", item === button));
  $$('[data-page]').forEach((page) => page.classList.toggle("active", page.dataset.page === button.dataset.section));
  $("#page-title").textContent = titles[button.dataset.section];
}));

$("#refresh-button").addEventListener("click", () => refreshAll().then(() => toast("状态已刷新")).catch((error) => toast(error.message, true)));

$$('[data-service]').forEach((button) => button.addEventListener("click", async () => {
  const action = button.dataset.service;
  if ((action === "stop" || action === "disable") && !window.confirm("确认执行此服务操作？")) return;
  button.disabled = true;
  try {
    await api(`/api/v1/service/${action}`, { method: "POST", body: {} });
    toast("服务操作已提交");
    setTimeout(() => refreshAll().catch(() => {}), 900);
  } catch (error) { toast(error.message, true); }
  finally { button.disabled = false; }
}));

$("#check-port-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const box = $("#port-result");
  try {
    const result = await api("/api/v1/network/check-port", { method: "POST", body: { host: form.elements.host.value, port: Number(form.elements.port.value) } });
    box.textContent = result.available ? `端口 ${result.port} 可用` : `端口不可用：${result.reason}`;
    box.classList.toggle("good", result.available); box.classList.toggle("bad", !result.available);
  } catch (error) { box.textContent = error.message; box.className = "result-box bad"; }
});

$("#ports-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  if (!window.confirm("保存端口会重启服务，是否继续？")) return;
  try {
    await api("/api/v1/config/ports", { method: "PUT", body: { http_port: Number(form.elements.http_port.value), https_port: Number(form.elements.https_port.value), web_port: Number(form.elements.web_port.value) } });
    toast("端口已保存，服务正在重启");
  } catch (error) { toast(error.message, true); }
});

$("#credentials-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  if (!window.confirm("重置后所有现有会话会失效，是否继续？")) return;
  try {
    const result = await api("/api/v1/admin/credentials", { method: "PUT", body: { username: form.elements.username.value, password: form.elements.password.value } });
    if (result.generated_password) {
      $("#generated-password").textContent = result.generated_password;
      $("#generated-secret").classList.remove("hidden");
    }
    toast("凭据已重置，请使用新凭据重新登录");
    setTimeout(showLogin, 1800);
  } catch (error) { toast(error.message, true); }
});

$("#check-update-button").addEventListener("click", async () => {
  try {
    state.update = await api("/api/v1/update/check", { method: "POST", body: { channel: "stable" } });
    $("#update-latest").textContent = state.update.latest;
    $("#apply-update-button").disabled = !state.privileged || !state.update.available;
    toast(state.update.available ? "发现新版本" : "当前已是最新版本");
  } catch (error) { toast(error.message, true); }
});

$("#apply-update-button").addEventListener("click", async () => {
  if (!state.update?.available || !window.confirm(`确认更新到 ${state.update.latest}？`)) return;
  try {
    await api("/api/v1/update/apply", { method: "POST", body: { version: state.update.latest, channel: "stable" } });
    toast("更新已安装，服务正在重启");
  } catch (error) { toast(error.message, true); }
});

(async function boot() {
  try { await api("/api/v1/auth/session"); showApp(); await refreshAll(); }
  catch { showLogin(); }
})();
