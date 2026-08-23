let messages = [
  {id:1,source:"gmail",icon:"✉",sender:"Maya Chen",subject:"Mission debrief — tomorrow",preview:"I pulled together the flight notes from today...",time:"12m",body:["I pulled together the flight notes from today’s run. The telemetry looks clean and I think we’re ready for the next window.","Can you review the navigation section before the 0900 briefing?"]},
  {id:2,source:"whatsapp",icon:"◌",sender:"Orbital Crew",subject:"Landing window confirmed",preview:"The new landing window is confirmed for 18:40.",time:"34m",body:["The new landing window is confirmed for 18:40. Everyone is clear to proceed."]},
  {id:3,source:"linkedin",icon:"in",sender:"Alex Rivera",subject:"Re: Spacebox platform",preview:"This is exactly the kind of interface our team needs.",time:"1h",body:["This is exactly the kind of interface our team needs. I’d love to compare notes on the integration layer."]},
  {id:4,source:"discord",icon:"◈",sender:"design-squad",subject:"HUD color pass",preview:"The ion-blue treatment reads beautifully on the dark hull.",time:"2h",body:["The ion-blue treatment reads beautifully on the dark hull. I’ve attached the latest HUD color pass for review."]},
  {id:5,source:"gmail",icon:"✉",sender:"Launch Control",subject:"Access credentials approved",preview:"Your relay access request has been approved.",time:"3h",body:["Your relay access request has been approved. Welcome aboard Launch Control."]}
];

const list = document.querySelector("#messages");
const search = document.querySelector("#search");
const accountSelect = document.querySelector("#account-select");
let activeFilter = "all";
let selectedAccount = localStorage.getItem("spacebox-account") || "";
let selectedId = null;
let nextPageToken = "";
let pageNumber = 1;
const pageTokens = [""];

function apiURL(path) {
  if (!selectedAccount) return path;
  return `${path}${path.includes("?") ? "&" : "?"}account=${encodeURIComponent(selectedAccount)}`;
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[<>&"]/g, char => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;" }[char]));
}

function renderInlineMarkdown(value) {
  let html = escapeHTML(value);
  html = html.replace(/`([^`\n]+)`/g, "<code>$1</code>");
  html = html.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer">$1</a>');
  html = html.replace(/\*\*([^*\n]+)\*\*|__([^_\n]+)__/g, (_, bold, underscored) => `<strong>${bold || underscored}</strong>`);
  html = html.replace(/(^|[^\*])\*([^*\n]+)\*(?!\*)/g, "$1<em>$2</em>");
  html = html.replace(/(^|[^_])_([^_\n]+)_(?!_)/g, "$1<em>$2</em>");
  return html;
}

function renderMarkdown(value) {
  const lines = String(value ?? "").replace(/\r\n?/g, "\n").split("\n");
  const blocks = [];
  let paragraph = [];
  let list = null;
  let code = null;

  const flushParagraph = () => {
    if (paragraph.length) {
      blocks.push(`<p>${paragraph.map(renderInlineMarkdown).join("<br>")}</p>`);
      paragraph = [];
    }
  };
  const flushList = () => {
    if (list) {
      blocks.push(`<${list.type}>${list.items.map(item => `<li>${renderInlineMarkdown(item)}</li>`).join("")}</${list.type}>`);
      list = null;
    }
  };

  for (const line of lines) {
    if (line.trim().startsWith("```")) {
      flushParagraph();
      flushList();
      if (code === null) code = [];
      else {
        blocks.push(`<pre><code>${escapeHTML(code.join("\n"))}</code></pre>`);
        code = null;
      }
      continue;
    }
    if (code !== null) {
      code.push(line);
      continue;
    }
    const heading = line.match(/^(#{1,3})\s+(.+)$/);
    const item = line.match(/^\s*([-*+]|\d+\.)\s+(.+)$/);
    if (heading) {
      flushParagraph();
      flushList();
      const level = heading[1].length;
      blocks.push(`<h${level}>${renderInlineMarkdown(heading[2])}</h${level}>`);
    } else if (item) {
      flushParagraph();
      const type = /^\d+\./.test(item[1]) ? "ol" : "ul";
      if (!list || list.type !== type) {
        flushList();
        list = { type, items: [] };
      }
      list.items.push(item[2]);
    } else if (/^\s*>\s?/.test(line)) {
      flushParagraph();
      flushList();
      blocks.push(`<blockquote>${renderInlineMarkdown(line.replace(/^\s*>\s?/, ""))}</blockquote>`);
    } else if (line.trim() === "") {
      flushParagraph();
      flushList();
    } else {
      paragraph.push(line);
    }
  }
  flushParagraph();
  flushList();
  if (code !== null) blocks.push(`<pre><code>${escapeHTML(code.join("\n"))}</code></pre>`);
  return blocks.join("");
}

function relativeDate(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const minutes = Math.max(1, Math.round((Date.now() - date.getTime()) / 60000));
  if (minutes < 60) return `${minutes}m`;
  if (minutes < 1440) return `${Math.round(minutes / 60)}h`;
  return `${Math.round(minutes / 1440)}d`;
}

function render() {
  const query = search.value.toLowerCase();
  const visible = messages.filter(m => (activeFilter === "all" || m.source === activeFilter) &&
    `${m.sender} ${m.subject} ${m.preview}`.toLowerCase().includes(query));
  list.innerHTML = visible.map((m, i) => `<article class="message ${i === 0 ? "selected" : ""}" data-id="${escapeHTML(m.id)}">
    <div class="avatar">${escapeHTML(m.icon)}</div><div><span class="source">${escapeHTML(m.source.toUpperCase())}</span><h3>${escapeHTML(m.sender)} · ${escapeHTML(m.subject)}</h3><p>${escapeHTML(m.preview)}</p></div><time>${escapeHTML(m.time)}</time>
  </article>`).join("");
  list.querySelectorAll(".message").forEach(el => el.addEventListener("click", () => select(el.dataset.id)));
  if (visible[0] && selectedId === null) select(visible[0].id);
}

async function select(id) {
  const message = messages.find(m => m.id === id);
  if (!message) return;
  selectedId = id;
  document.querySelector("#conversation-source").textContent = `${message.source.toUpperCase()} · ${message.time} AGO`;
  document.querySelector("#conversation-title").textContent = message.subject;
  document.querySelector("#conversation-body").innerHTML = `<div class="loading-state"><span class="radar"></span><strong>DECODING SIGNAL</strong><small>${escapeHTML(message.sender)} // ESTABLISHING SECURE LINK</small></div>`;
  document.querySelectorAll(".message").forEach(el => el.classList.toggle("selected", el.dataset.id === id));
  try {
    const response = await fetch(apiURL(`/api/threads/${encodeURIComponent(id)}`));
    if (!response.ok) return;
    const detail = await response.json();
    document.querySelector("#conversation-body").innerHTML = (detail.messages || []).map(item =>
      `<div class="bubble inbound"><div class="meta">${escapeHTML(item.from || message.sender)} // ${escapeHTML(item.date || "")}</div><div class="message-content">${renderMarkdown(item.body || "(No text content)")}</div></div>`).join("");
  } catch {
    // Keep the thread preview visible when detail loading is unavailable.
  }
}

async function loadLiveMessages() {
  try {
    const response = await fetch(apiURL("/api/threads"));
    if (!response.ok) return;
    const payload = await response.json();
    const liveThreads = Array.isArray(payload) ? payload : payload.threads;
    if (!Array.isArray(liveThreads) || liveThreads.length === 0) return;
    nextPageToken = payload.nextPageToken || "";
    document.querySelector('[data-count="all"]').textContent = payload.total ?? liveThreads.length;
    document.querySelector('[data-count="gmail"]').textContent = payload.unread ?? liveThreads.length;
    messages = liveThreads.map(thread => ({
      id: thread.id,
      source: "gmail",
      icon: "✉",
      sender: thread.from || "Gmail relay",
      subject: thread.subject || "(No subject)",
      preview: thread.snippet || "No preview available",
      time: relativeDate(thread.date),
      body: [thread.snippet || "Open this thread to view the full message."]
    }));
    render();
  } catch {
    // Keep the prototype messages visible when the API is unavailable.
  }

}

async function loadPage(pageToken = "") {
  selectedId = null;
  document.querySelector("#messages").innerHTML = `<div class="loading-state"><span class="radar"></span><strong>SCANNING RELAY DECK</strong><small>ACQUIRING MESSAGE SIGNALS...</small></div>`;
  document.querySelector("#previous-page").disabled = true;
  document.querySelector("#next-page").disabled = true;
  document.querySelector("#page-status").textContent = "SCANNING";
  const response = await fetch(apiURL(`/api/threads?${pageToken ? `pageToken=${encodeURIComponent(pageToken)}` : ""}`));
  if (!response.ok) return;
  const payload = await response.json();
  const liveThreads = payload.threads || [];
  messages = liveThreads.map(thread => ({
    id: thread.id, source: "gmail", icon: "✉", sender: thread.from || "Gmail relay",
    subject: thread.subject || "(No subject)", preview: thread.snippet || "No preview available",
    time: relativeDate(thread.date), body: [thread.snippet || "Open this thread to view the full message."]
  }));
  nextPageToken = payload.nextPageToken || "";
  document.querySelector('[data-count="all"]').textContent = payload.total ?? "—";
  document.querySelector('[data-count="gmail"]').textContent = payload.unread ?? "—";
  document.querySelector("#page-status").textContent = `RELAY ${String(pageNumber).padStart(2, "0")}`;
  document.querySelector("#previous-page").disabled = pageNumber === 1;
  document.querySelector("#next-page").disabled = !nextPageToken;
  render();
}

document.querySelectorAll(".nav-item[data-filter]").forEach(button => button.addEventListener("click", () => {
  activeFilter = button.dataset.filter;
  document.querySelectorAll(".nav-item").forEach(item => item.classList.remove("active"));
  button.classList.add("active");
  render();
}));
search.addEventListener("input", render);
accountSelect.addEventListener("change", () => {
  selectedAccount = accountSelect.value;
  localStorage.setItem("spacebox-account", selectedAccount);
  pageNumber = 1;
  pageTokens.length = 1;
  loadPage().catch(() => loadLiveMessages());
});
document.querySelector("#next-page").addEventListener("click", () => {
  if (!nextPageToken) return;
  pageTokens[pageNumber] = nextPageToken;
  pageNumber += 1;
  loadPage(nextPageToken);
});
document.querySelector("#previous-page").addEventListener("click", () => {
  if (pageNumber <= 1) return;
  pageNumber -= 1;
  loadPage(pageTokens[pageNumber - 2]);
});
document.querySelector("#reply-form").addEventListener("submit", event => {
  event.preventDefault();
  const reply = document.querySelector("#reply");
  if (!reply.value.trim()) return;
  const bubble = document.createElement("div");
  bubble.className = "bubble outbound";
  bubble.innerHTML = `<div class="meta">YOU // JUST NOW</div><div class="message-content">${renderMarkdown(reply.value)}</div>`;
  document.querySelector("#conversation-body").append(bubble);
  reply.value = "";
});
render();

async function loadAccounts() {
  const response = await fetch("/api/accounts");
  if (!response.ok) throw new Error("Could not load Gmail accounts");
  const accounts = await response.json();
  accountSelect.innerHTML = "";
  accounts.forEach(account => {
    const option = document.createElement("option");
    option.value = account.id;
    option.textContent = account.email;
    accountSelect.append(option);
  });
  if (!accounts.length) {
    accountSelect.innerHTML = '<option value="">No Gmail accounts</option>';
    return;
  }
  if (!accounts.some(account => account.id === selectedAccount)) selectedAccount = accounts[0].id;
  accountSelect.value = selectedAccount;
  localStorage.setItem("spacebox-account", selectedAccount);
}

loadAccounts().catch(() => {
  accountSelect.innerHTML = '<option value="">Account service unavailable</option>';
}).finally(() => {
  loadPage().catch(() => loadLiveMessages());
});
