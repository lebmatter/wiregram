try {
  const saved = localStorage.getItem('wiregram-theme');
  if (saved === 'light' || saved === 'dark') document.documentElement.dataset.theme = saved;
} catch (_) {}

function wiregramInit() {
  let csrfToken = '';
  let sse = null;

  async function api(method, path, body) {
    const opts = {
      method,
      headers: { 'X-Wiregram-CSRF': csrfToken },
    };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    const res = await fetch('/api' + path, opts);
    if (!res.ok) throw new Error(await res.text() || res.statusText);
    if (res.status === 204) return null;
    return res.json();
  }

  function formatTime(ts) {
    if (!ts) return '';
    const d = new Date(ts);
    if (isNaN(d.getTime())) return '';
    const now = new Date();
    const diff = now - d;
    if (diff < 86400000 && d.getDate() === now.getDate()) {
      return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false });
    }
    if (diff < 604800000) return d.toLocaleDateString([], { weekday: 'short' });
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  }

  function msgTime(ts) {
    if (!ts) return '';
    const d = new Date(ts);
    if (isNaN(d.getTime())) return '';
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false });
  }

  function initials(name) {
    if (!name) return '?';
    const parts = name.replace(/^\[.+?\]\s*/, '').trim().split(/\s+/);
    if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
    return name.slice(0, 2).toUpperCase();
  }

  function enrichMsg(m) {
    return { ...m, _time: msgTime(m.timestamp), _ini: m.outgoing ? 'ME' : initials(m.sender) };
  }

  function scrollDown() {
    const el = document.querySelector('.messages');
    if (el) el.scrollTop = el.scrollHeight;
  }

  // ---- SSE ----

  function connectSSE() {
    if (sse) { sse.close(); sse = null; }
    if (!csrfToken) { setTimeout(connectSSE, 500); return; }
    const es = new EventSource('/api/events?csrf=' + encodeURIComponent(csrfToken));

    es.addEventListener('message', e => {
      try {
        const d = JSON.parse(e.data);
        const c = Alpine.store('chat');
        if (c.accountIdx == d.account && c.selectedChat === d.chat_id && d.message) {
          const m = enrichMsg(d.message);
          if (!c.messages.some(x => x.id === m.id)) {
            c.messages.push(m);
            requestAnimationFrame(scrollDown);
          }
        }
        bumpUnread(d.account, d.chat_id);
      } catch (_) {}
    });
    es.addEventListener('edit', e => {
      try {
        const d = JSON.parse(e.data);
        const c = Alpine.store('chat');
        if (c.accountIdx == d.account && c.selectedChat === d.chat_id && d.message) {
          const i = c.messages.findIndex(m => m.id === d.message.id);
          if (i >= 0) c.messages[i] = enrichMsg(d.message);
        }
      } catch (_) {}
    });
    es.addEventListener('delete', e => {
      try {
        const d = JSON.parse(e.data);
        const c = Alpine.store('chat');
        if (c.accountIdx == d.account && c.selectedChat === d.chat_id)
          c.messages = c.messages.filter(m => m.id !== d.message_id);
      } catch (_) {}
    });
    es.addEventListener('reaction', e => {
      try {
        const d = JSON.parse(e.data);
        const c = Alpine.store('chat');
        if (c.accountIdx == d.account && c.selectedChat === d.chat_id) {
          const m = c.messages.find(m => m.id === d.message_id);
          if (m) m.reactions = d.reactions || [];
        }
      } catch (_) {}
    });
    es.addEventListener('accounts', e => {
      try {
        const d = JSON.parse(e.data);
        Alpine.store('app').accounts = d;
      } catch (_) {}
    });
    es.addEventListener('unread', e => {
      try {
        const d = JSON.parse(e.data);
        const c = Alpine.store('chat');
        if (c.accountIdx == d.account) {
          const ch = c.chats.find(x => x.id === d.chat_id);
          if (ch) ch.unread = d.count;
        }
      } catch (_) {}
    });
    es.addEventListener('suggestion', e => {
      try {
        const d = JSON.parse(e.data);
        const s = Alpine.store('app').suggestions;
        const i = s.findIndex(x => x.key === d.key);
        if (i >= 0) s[i] = d; else s.push(d);
      } catch (_) {}
    });
    es.addEventListener('suggestion_gone', e => {
      try {
        const d = JSON.parse(e.data);
        Alpine.store('app').suggestions = Alpine.store('app').suggestions.filter(s => s.key !== d.key);
      } catch (_) {}
    });
    es.addEventListener('ping', () => {});
    es.onerror = () => { es.close(); sse = null; setTimeout(connectSSE, 3000); };
    sse = es;
  }

  function bumpUnread(acct, chatId) {
    const c = Alpine.store('chat');
    if (c.accountIdx != acct || c.selectedChat === chatId) return;
    const ch = c.chats.find(x => x.id === chatId);
    if (ch) ch.unread = (ch.unread || 0) + 1;
  }

  // ---- Stores ----

  Alpine.store('app', {
    mode: 'chat',
    accounts: [],
    suggestions: [],
    showAccounts: false,
    theme: document.documentElement.dataset.theme,
    toggleTheme() {
      this.theme = this.theme === 'light' ? 'dark' : 'light';
      document.documentElement.dataset.theme = this.theme;
      try { localStorage.setItem('wiregram-theme', this.theme); } catch (_) {}
    },
    async init() {
      try { csrfToken = (await api('GET', '/session')).csrf; } catch (_) {}
      await this.loadAccounts();
      connectSSE();
      Alpine.store('chat').loadChats();
    },
    async loadAccounts() {
      try { this.accounts = await api('GET', '/accounts'); }
      catch (_) { this.accounts = []; }
    },
  });

  Alpine.store('chat', {
    accountIdx: 0,
    chats: [],
    selectedChat: null,
    messages: [],
    draft: '',
    replyTo: null,
    loading: false,
    loadingOlder: false,
    searchQuery: '',
    sidebarOpen: false,

    get currentTitle() {
      const c = this.chats.find(x => x.id === this.selectedChat);
      return c ? c.title : '';
    },
    get filtered() {
      if (!this.searchQuery) return this.chats;
      const q = this.searchQuery.toLowerCase();
      return this.chats.filter(c => c.title.toLowerCase().includes(q));
    },

    async loadChats() {
      this.selectedChat = null;
      this.messages = [];
      try {
        const raw = await api('GET', '/accounts/' + this.accountIdx + '/chats');
        this.chats = (raw || []).map(c => ({ ...c, _time: formatTime(c.lastAt), _ini: initials(c.title) }));
      } catch (_) { this.chats = []; }
    },
    async selectChat(id) {
      this.selectedChat = id;
      this.messages = [];
      this.replyTo = null;
      this.sidebarOpen = false;
      this.loading = true;
      try {
        const raw = await api('GET',
          '/accounts/' + this.accountIdx + '/chats/' + encodeURIComponent(id) + '/messages');
        this.messages = (raw || []).map(enrichMsg);
        requestAnimationFrame(scrollDown);
      } catch (_) { this.messages = []; }
      this.loading = false;
    },
    async loadOlder() {
      if (this.loadingOlder || !this.messages.length || !this.selectedChat) return;
      this.loadingOlder = true;
      try {
        const raw = await api('GET',
          '/accounts/' + this.accountIdx + '/chats/' + encodeURIComponent(this.selectedChat) +
          '/messages?before=' + encodeURIComponent(this.messages[0].id));
        if (raw && raw.length) this.messages = [...raw.map(enrichMsg), ...this.messages];
      } catch (_) {}
      this.loadingOlder = false;
    },
    async send() {
      const text = this.draft.trim();
      if (!text || !this.selectedChat) return;
      const body = { text };
      if (this.replyTo) body.reply_to = this.replyTo.id;
      this.draft = '';
      const savedReply = this.replyTo;
      this.replyTo = null;
      try {
        await api('POST',
          '/accounts/' + this.accountIdx + '/chats/' + encodeURIComponent(this.selectedChat) + '/messages',
          body);
      } catch (_) { this.draft = text; this.replyTo = savedReply; }
    },
  });

  Alpine.store('agents', {
    tab: 'agents',
    agents: [],
    providers: [],
    editing: null,
    form: {},

    async init() {
      await Promise.all([this.loadAgents(), this.loadProviders()]);
    },
    async loadAgents() {
      try { this.agents = await api('GET', '/agents'); } catch (_) { this.agents = []; }
    },
    async loadProviders() {
      try { this.providers = await api('GET', '/providers'); } catch (_) { this.providers = []; }
    },

    newAgent() {
      this.editing = { type: 'agent', isNew: true };
      this.form = {
        name: '', prompt: '', autoreply: false,
        provider_id: this.providers.length ? this.providers[0].id : 0,
        retention_days: 30, enabled: true, knowledge: '', chats: [],
      };
    },
    editAgent(a) {
      this.editing = { type: 'agent', isNew: false, id: a.id };
      this.form = {
        name: a.name, prompt: a.prompt || '', autoreply: !!a.autoreply,
        provider_id: a.provider_id || a.providerID || 0,
        retention_days: a.retention_days || a.retentionDays || 30,
        enabled: a.enabled !== false, knowledge: '', chats: [],
      };
      this.loadAgentSub(a.id);
    },
    async loadAgentSub(id) {
      try {
        const [k, c] = await Promise.all([
          api('GET', '/agents/' + id + '/knowledge'),
          api('GET', '/agents/' + id + '/chats'),
        ]);
        this.form.knowledge = (k || []).join('\n');
        this.form.chats = c || [];
      } catch (_) {}
    },
    async saveAgent() {
      const f = this.form;
      const body = {
        name: f.name, prompt: f.prompt, autoreply: f.autoreply ? 1 : 0,
        provider_id: Number(f.provider_id), retention_days: Number(f.retention_days) || 30,
        enabled: f.enabled,
      };
      try {
        if (this.editing.isNew) await api('POST', '/agents', body);
        else await api('PUT', '/agents/' + this.editing.id, body);
        await this.loadAgents();
        this.editing = null;
      } catch (_) {}
    },
    async deleteAgent(id) {
      if (!confirm('Delete this agent?')) return;
      try {
        await api('DELETE', '/agents/' + id);
        await this.loadAgents();
        if (this.editing && this.editing.id === id) this.editing = null;
      } catch (_) {}
    },

    newProvider() {
      this.editing = { type: 'provider', isNew: true };
      this.form = { name: '', base_url: '', api_key_env: '', model: '' };
    },
    editProvider(p) {
      this.editing = { type: 'provider', isNew: false, id: p.id };
      this.form = {
        name: p.name, base_url: p.base_url || p.baseURL || '',
        api_key_env: p.api_key_env || p.apiKeyEnv || '', model: p.model || '',
      };
    },
    async saveProvider() {
      const f = this.form;
      const body = { name: f.name, base_url: f.base_url, api_key_env: f.api_key_env, model: f.model };
      try {
        if (this.editing.isNew) await api('POST', '/providers', body);
        else await api('PUT', '/providers/' + this.editing.id, body);
        await this.loadProviders();
        this.editing = null;
      } catch (_) {}
    },
    async deleteProvider(id) {
      if (!confirm('Delete this provider?')) return;
      try {
        await api('DELETE', '/providers/' + id);
        await this.loadProviders();
        if (this.editing && this.editing.id === id) this.editing = null;
      } catch (_) {}
    },
  });
}

document.addEventListener('alpine:init', () => {
  try {
    wiregramInit();
  } catch (e) {
    console.error('wiregram: alpine:init failed:', e);
  }
});
