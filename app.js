let messages = [
  {id:1,source:"gmail",icon:"✉",sender:"Maya Chen",subject:"Mission debrief — tomorrow",preview:"I pulled together the flight notes from today...",time:"12m",body:["I pulled together the flight notes from today’s run. The telemetry looks clean and I think we’re ready for the next window.","Can you review the navigation section before the 0900 briefing?"]},
  {id:2,source:"whatsapp",icon:"◌",sender:"Orbital Crew",subject:"Landing window confirmed",preview:"The new landing window is confirmed for 18:40.",time:"34m",body:["The new landing window is confirmed for 18:40. Everyone is clear to proceed."]},
  {id:3,source:"linkedin",icon:"in",sender:"Alex Rivera",subject:"Re: Spacebox platform",preview:"This is exactly the kind of interface our team needs.",time:"1h",body:["This is exactly the kind of interface our team needs. I’d love to compare notes on the integration layer."]},
  {id:4,source:"discord",icon:"◈",sender:"design-squad",subject:"HUD color pass",preview:"The ion-blue treatment reads beautifully on the dark hull.",time:"2h",body:["The ion-blue treatment reads beautifully on the dark hull. I’ve attached the latest HUD color pass for review."]},
  {id:5,source:"gmail",icon:"✉",sender:"Launch Control",subject:"Access credentials approved",preview:"Your relay access request has been approved.",time:"3h",body:["Your relay access request has been approved. Welcome aboard Launch Control."]}
];

const list = document.querySelector("#messages");
const search = document.querySelector("#search");
let activeFilter = "all";
let selectedId = null;
let nextPageToken = "";
let pageNumber = 1;
const pageTokens = [""];

function escapeHTML(value) {
  return String(value ?? "").replace(/[<>&"]/g, char => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;" }[char]));
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
  document.querySelector("#conversation-body").innerHTML = `<div class="bubble inbound"><div class="meta">${escapeHTML(message.sender)} // LOADING THREAD</div><p>${escapeHTML(message.preview)}</p></div>`;
  document.querySelectorAll(".message").forEach(el => el.classList.toggle("selected", el.dataset.id === id));
  try {
    const response = await fetch(`/api/threads/${encodeURIComponent(id)}`);
    if (!response.ok) return;
    const detail = await response.json();
    document.querySelector("#conversation-body").innerHTML = (detail.messages || []).map(item =>
      `<div class="bubble inbound"><div class="meta">${escapeHTML(item.from || message.sender)} // ${escapeHTML(item.date || "")}</div><p>${escapeHTML(item.body || "(No text content)")}</p></div>`).join("");
  } catch {
    // Keep the thread preview visible when detail loading is unavailable.
  }
}

async function loadLiveMessages() {
  try {
    const response = await fetch("/api/threads");
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

  async function loadPage(pageToken = "") {
    selectedId = null;
    const response = await fetch(`/api/threads?${pageToken ? `pageToken=${encodeURIComponent(pageToken)}` : ""}`);
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
}

document.querySelectorAll(".nav-item[data-filter]").forEach(button => button.addEventListener("click", () => {
  activeFilter = button.dataset.filter;
  document.querySelectorAll(".nav-item").forEach(item => item.classList.remove("active"));
  button.classList.add("active");
  render();
}));
search.addEventListener("input", render);
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
  bubble.innerHTML = `<div class="meta">YOU // JUST NOW</div><p>${reply.value.replace(/[<>&]/g, c => ({'<':"&lt;",'>':"&gt;","&":"&amp;"}[c]))}</p>`;
  document.querySelector("#conversation-body").append(bubble);
  reply.value = "";
});
render();
loadPage().catch(() => loadLiveMessages());
