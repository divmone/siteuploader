"use strict";

const $ = id => document.getElementById(id);
const token = location.hash.slice(1) || sessionStorage.getItem("site-uploader");
if (location.hash && token) {
  sessionStorage.setItem("site-uploader", token);
  history.replaceState(null, "", "/");
}

const fieldIDs = {host: "host", port: "port", user: "user", start_dir: "start-dir", key_path: "key-path"};
const dateFormat = new Intl.DateTimeFormat("ru", {dateStyle: "medium", timeStyle: "short"});
const sizeFormat = new Intl.NumberFormat("ru", {maximumFractionDigits: 1});
const editor = $("editor");
const promptDialog = $("prompt");
let state = {profiles: [], files: [], address: "", directory: ""};
let activeProfile = "";
let selected = "";
let busy = false;
let closed = false;
let draft = null;

async function api(op, data = {}, query = "") {
  const form = data instanceof FormData;
  const headers = {"X-Site-Uploader": token || ""};
  if (!form) headers["Content-Type"] = "application/json";
  const response = await fetch(`/api/${op}${query}`, {
    method: "POST", headers, body: form ? data : JSON.stringify(data), cache: "no-store"
  });
  const result = await response.json();
  if (!response.ok) throw Object.assign(new Error(result.error || "Операция не завершена"), result);
  return result;
}

function notice(text = "", error = false) {
  const box = $("notice");
  box.textContent = text;
  box.hidden = !text;
  box.classList.toggle("error", error);
}

async function task(work) {
  if (busy || closed) return;
  busy = true;
  toggleBusy();
  notice();
  try {
    await work();
  } catch (error) {
    notice(error.message, true);
  } finally {
    busy = false;
    toggleBusy();
  }
}

function toggleBusy() {
  for (const panel of [$("setup"), $("workspace"), $("profiles")]) panel.inert = busy || closed;
  for (const id of ["new-profile", "quit", "disconnect"]) $(id).disabled = busy || closed || (id === "new-profile" && Boolean(state.address));
  document.querySelector("main").setAttribute("aria-busy", String(busy));
}

function ask({title, detail = "", initial = null, ok = "Готово"}) {
  $("prompt-title").textContent = title;
  $("prompt-detail").textContent = detail;
  $("prompt-detail").hidden = !detail;
  const input = $("prompt-input");
  input.hidden = initial === null;
  input.required = initial !== null;
  input.value = initial ?? "";
  $("prompt-ok").textContent = ok;
  promptDialog.returnValue = "";
  promptDialog.showModal();
  if (initial !== null) {
    input.focus();
    input.select();
  } else {
    promptDialog.querySelector('[value="cancel"]').focus();
  }
  return new Promise(resolve => promptDialog.addEventListener("close", () => {
    resolve(promptDialog.returnValue === "ok" ? (initial === null ? true : input.value) : null);
  }, {once: true}));
}

function formData() {
  const fields = {profile: activeProfile, remember: $("remember").checked, secret: $("secret").value};
  for (const [field, id] of Object.entries(fieldIDs)) fields[field] = $(id).value.trim();
  return fields;
}

function credentialHint() {
  const current = state.profiles.find(p => p.name === activeProfile);
  const form = formData();
  const saved = form.remember && current?.saved && current.host.toLowerCase() === form.host.toLowerCase() &&
    Number(current.port) === Number(form.port) && current.user === form.user && (current.key_path || "") === form.key_path;
  $("secret").placeholder = saved ? "Сохранённый пароль" : "";
}

function selectProfile(name, fallback = {}) {
  const profile = state.profiles.find(p => p.name === name);
  activeProfile = profile?.name || "";
  const fields = profile || fallback;
  for (const [field, id] of Object.entries(fieldIDs)) $(id).value = fields[field] || (field === "port" ? "22" : "");
  $("secret").value = "";
  $("remember").checked = Boolean(profile?.saved);
  credentialHint();
  render();
}

function applyState(next) {
  if (next.directory !== state.directory) {
    selected = "";
    $("search").value = "";
  }
  state = next;
  $("remote-path").value = state.directory;
  if (next.warning) notice(next.warning, true);
  render();
}

function render() {
  const connected = Boolean(state.address);
  const profileList = state.profiles.map(profile => {
    const button = document.createElement("button");
    button.className = "profile-button";
    button.type = "button";
    button.disabled = connected;
    button.title = profile.name;
    button.setAttribute("aria-current", String(profile.name === activeProfile));
    const dot = document.createElement("span");
    dot.className = "profile-dot";
    dot.setAttribute("aria-hidden", "true");
    const label = document.createElement("span");
    label.textContent = profile.name;
    button.append(dot, label);
    button.addEventListener("click", () => { notice(); selectProfile(profile.name); });
    return button;
  });
  $("profiles").replaceChildren(...profileList);
  $("connection-state").classList.toggle("online", connected);
  $("connection-state").lastChild.textContent = connected ? "Подключено" : "Отключено";
  $("section-label").textContent = connected ? state.address : "Сервер";
  $("page-title").textContent = connected ? (activeProfile || "Файлы сервера") : (activeProfile || "Подключение");
  $("setup").hidden = connected;
  $("workspace").hidden = !connected || closed;
  $("disconnect").hidden = !connected || closed;
  $("delete-profile").hidden = !activeProfile;
  $("up").disabled = !state.directory || state.directory === "/";
  for (const id of ["upload", "mkdir"]) $(id).disabled = !state.directory;
  renderFiles();
  toggleBusy();
}

function fileSize(bytes) {
  let size = bytes;
  for (const unit of ["Б", "КиБ", "МиБ", "ГиБ", "ТиБ"]) {
    if (size < 1024 || unit === "ТиБ") return `${sizeFormat.format(size)} ${unit}`;
    size /= 1024;
  }
}

function renderFiles() {
  const files = state.files || [];
  const filter = $("search").value.toLocaleLowerCase();
  const visible = files.filter(file => file.name.toLocaleLowerCase().includes(filter));
  if (!visible.some(file => file.name === selected)) selected = "";
  const fragment = document.createDocumentFragment();
  for (const file of visible) {
    const row = document.createElement("tr");
    row.dataset.name = file.name;
    row.setAttribute("aria-selected", String(file.name === selected));
    const name = row.insertCell();
    const button = document.createElement("button");
    button.className = "file-name";
    button.type = "button";
    const icon = document.createElement("span");
    icon.className = file.kind === "folder" ? "folder-icon" : "file-icon";
    icon.textContent = file.kind === "link" ? "↗" : (file.kind === "folder" ? "" : "▤");
    icon.setAttribute("aria-hidden", "true");
    button.append(icon, document.createTextNode(file.name));
    name.append(button);
    row.insertCell().textContent = file.kind === "file" ? fileSize(file.size) : "—";
    row.insertCell().textContent = dateFormat.format(new Date(file.modified));
    row.addEventListener("click", () => selectFile(file.name));
    row.addEventListener("dblclick", () => activateFile(file));
    button.addEventListener("keydown", event => {
      if (event.key === "Enter") { event.preventDefault(); activateFile(file); }
    });
    fragment.append(row);
  }
  $("file-list").replaceChildren(fragment);
  $("empty").hidden = visible.length > 0;
  $("empty-text").textContent = filter ? "Ничего не найдено" : (state.directory ? "Папка пуста" : "Откройте папку на сервере");
  $("count").textContent = filter ? `${visible.length} из ${files.length}` : `Объектов: ${files.length}`;
  selectFile(selected);
}

function selection() { return (state.files || []).find(file => file.name === selected); }
function selectFile(name) {
  selected = name;
  for (const row of $("file-list").rows) row.setAttribute("aria-selected", String(row.dataset.name === name));
  const file = selection();
  $("selection-bar").hidden = !file;
  $("selection-name").textContent = file?.name || "";
  $("open-file").hidden = !file || !["folder", "link"].includes(file.kind);
  $("download").hidden = file?.kind !== "file";
  $("edit").hidden = file?.kind !== "file" || file.size > 1048576;
}

function navigate(path) { return task(async () => applyState(await api("list", {path}))); }
function activateFile(file) {
  if (!file) return;
  if (["folder", "link"].includes(file.kind)) return navigate(`${state.directory.replace(/\/$/, "")}/${file.name}`);
  if (file.kind === "file" && file.size <= 1048576) return edit(file);
}

function edit(file) {
  return task(async () => {
    const {text, digest} = await api("read-text", {name: file.name});
    const bom = text.startsWith("\uFEFF") ? "\uFEFF" : "";
    $("editor-name").textContent = file.name;
    $("editor-text").value = text.slice(bom.length);
    draft = {name: file.name, digest, bom, crlf: text.includes("\r\n"), initial: $("editor-text").value};
    $("editor-error").hidden = true;
    editor.showModal();
  });
}

async function closeEditor() {
  if (busy) return;
  if (draft && $("editor-text").value !== draft.initial && !await ask({title: "Закрыть без сохранения?", ok: "Закрыть"})) return;
  editor.close();
  draft = null;
}

function upload(files) {
  const list = [...files];
  if (!state.directory || !list.length) return;
  return task(async () => {
    const errors = [];
    let uploaded = 0;
    for (const [index, file] of list.entries()) {
      notice(`Загрузка ${index + 1} / ${list.length} · ${file.name}`);
      const payload = new FormData();
      payload.append("file", file, file.name);
      try {
        let next;
        try {
          next = await api("upload", payload);
        } catch (error) {
          if (!error.collision) throw error;
          if (!await ask({title: "Заменить файл?", detail: file.name, ok: "Заменить"})) continue;
          next = await api("upload", payload, "?replace=yes");
        }
        applyState(next);
        uploaded++;
      } catch (error) {
        errors.push(`${file.name} — ${error.message}`);
      }
    }
    const message = [`Загружено файлов: ${uploaded} из ${list.length}`, ...errors.slice(0, 8)];
    if (errors.length > 8) message.push(`Других ошибок: ${errors.length - 8}`);
    notice(message.join("\n"), errors.length > 0);
  });
}

$("new-profile").addEventListener("click", () => { notice(); selectProfile(""); $("host").focus(); });
for (const id of [...Object.values(fieldIDs), "remember"]) $(id).addEventListener("input", credentialHint);
$("connection-form").addEventListener("submit", event => {
  event.preventDefault();
  task(async () => {
    const input = formData();
    notice("Подключение к серверу…");
    let next;
    try {
      next = await api("connect", input);
    } catch (error) {
      if (!error.fingerprint) throw error;
      const accepted = await ask({title: "Проверка ключа SSH", detail: `Сверьте отпечаток ключа с администратором сервера.\n\n${error.fingerprint}`, ok: "Доверять и подключиться"});
      if (!accepted) { notice("Подключение отменено."); return; }
      next = await api("connect", {...input, fingerprint: error.fingerprint});
    }
    $("secret").value = "";
    notice();
    applyState(next);
  });
});

$("save-profile").addEventListener("click", () => {
  if (!$("connection-form").reportValidity()) return;
  task(async () => {
    const input = formData();
    const name = await ask({title: "Сохранить профиль", initial: activeProfile || input.host, ok: "Сохранить"});
    if (name === null) return;
    applyState(await api("save-profile", {...input, name}));
    selectProfile(state.last_profile);
    if (!input.remember) $("secret").value = input.secret;
    notice(`Профиль «${state.last_profile}» сохранён.`);
  });
});
$("delete-profile").addEventListener("click", () => task(async () => {
  if (!await ask({title: "Удалить профиль?", detail: activeProfile, ok: "Удалить"})) return;
  applyState(await api("delete-profile", {profile: activeProfile}));
  selectProfile("");
  notice("Профиль удалён.");
}));
$("disconnect").addEventListener("click", () => task(async () => applyState(await api("disconnect"))));
$("quit").addEventListener("click", () => task(async () => {
  await api("quit");
  closed = true;
  notice("Приложение завершило работу. Можно закрыть вкладку.");
  $("setup").hidden = true;
  $("workspace").hidden = true;
  $("disconnect").hidden = true;
}));
$("path-form").addEventListener("submit", event => { event.preventDefault(); navigate($("remote-path").value); });
$("up").addEventListener("click", () => navigate(state.directory.slice(0, state.directory.lastIndexOf("/")) || "/"));
$("refresh").addEventListener("click", () => navigate(state.directory));
$("search").addEventListener("input", renderFiles);
$("workspace").addEventListener("keydown", event => {
  if (busy || event.ctrlKey || event.altKey || event.metaKey) return;
  if (event.key === "F5") {
    event.preventDefault();
    navigate(state.directory);
  } else if (selection() && !event.target.matches("input, textarea")) {
    if (event.key === "F2") $("move").click();
    if (event.key === "Delete") $("remove").click();
  }
});
$("upload").addEventListener("click", () => $("file-input").click());
$("file-input").addEventListener("change", event => { upload(event.target.files); event.target.value = ""; });
const drop = $("file-drop");
drop.addEventListener("dragover", event => {
  if (!event.dataTransfer.types.includes("Files")) return;
  event.preventDefault();
  drop.classList.toggle("dragging", Boolean(state.directory) && !busy);
});
drop.addEventListener("dragleave", event => { if (!drop.contains(event.relatedTarget)) drop.classList.remove("dragging"); });
drop.addEventListener("drop", event => { event.preventDefault(); drop.classList.remove("dragging"); upload(event.dataTransfer.files); });

$("mkdir").addEventListener("click", () => task(async () => {
  const name = await ask({title: "Новая папка", initial: "", ok: "Создать"});
  if (name !== null) applyState(await api("mkdir", {name}));
}));
$("open-file").addEventListener("click", () => activateFile(selection()));
$("download").addEventListener("click", () => task(async () => {
  const file = selection();
  if (!file) return;
  const {url} = await api("download-ticket", {name: file.name});
  const link = document.createElement("a");
  link.href = url;
  link.download = "";
  document.body.append(link);
  link.click();
  link.remove();
  notice(`Скачивание: ${file.name}. Ход загрузки виден в браузере.`);
}));
$("edit").addEventListener("click", () => { const file = selection(); if (file) edit(file); });
$("move").addEventListener("click", () => task(async () => {
  const file = selection();
  if (!file) return;
  const path = await ask({title: "Переместить или переименовать", detail: "Введите новое имя или путь на сервере вместе с именем.", initial: file.name, ok: "Сохранить"});
  if (path !== null && path !== file.name) applyState(await api("move", {name: file.name, path}));
}));
$("remove").addEventListener("click", () => task(async () => {
  const file = selection();
  if (!file) return;
  const detail = file.kind === "folder" ? `${file.name}\nУдалить можно только пустую папку.` : file.name;
  if (await ask({title: "Удалить с сервера?", detail, ok: "Удалить"})) applyState(await api("remove", {name: file.name}));
}));

$("editor-close").addEventListener("click", closeEditor);
editor.addEventListener("cancel", event => { event.preventDefault(); closeEditor(); });
$("editor-save").addEventListener("click", () => task(async () => {
  if (!draft) return;
  let text = $("editor-text").value;
  if (text === draft.initial) { editor.close(); draft = null; return; }
  if (draft.crlf) text = text.replace(/\n/g, "\r\n");
  $("editor-save").disabled = true;
  $("editor-text").readOnly = true;
  $("editor-error").hidden = true;
  try {
    applyState(await api("write-text", {name: draft.name, digest: draft.digest, text: draft.bom + text}));
    editor.close();
    draft = null;
    notice("Файл сохранён на сервере.");
  } catch (error) {
    $("editor-error").textContent = error.message;
    $("editor-error").hidden = false;
  } finally {
    $("editor-save").disabled = false;
    $("editor-text").readOnly = false;
  }
}));

if (token) {
  task(async () => {
    applyState(await api("state"));
    selectProfile(state.last_profile, state.last_connection);
  });
} else {
  closed = true;
  toggleBusy();
  notice("Откройте приложение через файл Site Uploader.", true);
}
