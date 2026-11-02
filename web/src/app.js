// ForgeSI console: a repository workspace. The plan (todo.md) sits on
// the left; the activity stream — runs, proposals, commits — flows on
// the right. Reads are anonymous; writes require an atproto OAuth
// session (sign-in modal opens the moment you try to act, or tap the
// header chip). Authenticated calls mint a short-lived service-auth
// JWT from your PDS (aud = the repo DID or the knot DID, lxm = the
// method) — exactly what the Tangled web app does with knots.
import {
	configureOAuth,
	createAuthorizationUrl,
	finalizeAuthorization,
	getSession,
	deleteStoredSession,
	OAuthUserAgent,
} from '@atcute/oauth-browser-client';
import {
	CompositeDidDocumentResolver,
	LocalActorResolver,
	PlcDidDocumentResolver,
	WebDidDocumentResolver,
	XrpcDidDocumentResolver,
	XrpcHandleResolver,
} from '@atcute/identity-resolver';

const KNOT_DID = 'did:web:forgesi.org';
const SCOPE = 'atproto repo:org.tangled.* rpc:com.atproto.server.getServiceAuth?aud=* rpc:com.atproto.repo.createRecord?aud=* rpc:com.atproto.repo.putRecord?aud=* rpc:com.atproto.repo.deleteRecord?aud=* rpc:org.tangled.review.submitPatch?aud=* rpc:org.tangled.repo.mintToken?aud=* rpc:org.tangled.git.receivePack?aud=* rpc:com.atproto.server.getServiceAuth?aud=*';

let agent = null; // OAuthUserAgent when signed in
let agentDID = null;

const didDocumentResolver = new CompositeDidDocumentResolver({
	methods: {
		plc: new PlcDidDocumentResolver(),
		web: new WebDidDocumentResolver(),
		xrpc: new XrpcDidDocumentResolver({ serviceUrl: 'https://public.api.bsky.app' }),
	},
});
// Browser-side resolution: XRPC resolvers (bsky AppView + plc.directory)
// send CORS headers; raw DoH/well-known fetches to arbitrary hosts don't.
const handleResolver = new XrpcHandleResolver({ serviceUrl: 'https://public.api.bsky.app' });

const $ = (id) => document.getElementById(id);
let repos = [], current = null;
let todoText = '', todoHead = null;
let runItems = [];        // fleet runs (raw)
let proposalRecs = [];    // ticket records with targetBranch
let liveItems = [];       // {ts, text, title} firehose commits

function spinEl(small) {
	const el = document.createElement('span');
	el.className = 'spin' + (small ? ' sm' : '');
	return el;
}

function skeletonRows(n, slim) {
	const frag = document.createDocumentFragment();
	for (let i = 0; i < n; i++) {
		const el = document.createElement('div');
		el.className = 'skel' + (slim ? ' slim' : '');
		frag.appendChild(el);
	}
	return frag;
}

async function busy(btn, fn) {
	if (!btn) return fn();
	btn.classList.add('busy');
	btn.disabled = true;
	try {
		return await fn();
	} finally {
		btn.classList.remove('busy');
		btn.disabled = false;
	}
}

function log(msg, cls) {
	const f = $('flash');
	if (f) { f.textContent = msg || ''; f.className = 'flash ' + (cls || ''); }
	console.log('[console]', msg);
}

function emptyEl(text) {
	return Object.assign(document.createElement('div'), { className: 'empty', textContent: text });
}

function timeago(iso) {
	const t = Date.parse(iso);
	if (!t || Number.isNaN(t)) return '';
	const s = Math.max(1, Math.floor((Date.now() - t) / 1000));
	if (s < 60) return s + 's';
	if (s < 3600) return Math.floor(s / 60) + 'm';
	if (s < 86400) return Math.floor(s / 3600) + 'h';
	return Math.floor(s / 86400) + 'd';
}

// ---------- auth ----------

let oauthReady = false;
function ensureOAuth() {
	if (oauthReady) return;
	configureOAuth({
		metadata: {
			client_id: 'https://forgesi.org/oauth/client-metadata.json',
			redirect_uri: 'https://forgesi.org/oauth/callback/',
		},
		identityResolver: new LocalActorResolver({
			handleResolver,
			didDocumentResolver,
		}),
	});
	oauthReady = true;
}

async function resumeSession() {
	const did = sessionStorage.getItem('forgesi-did');
	if (!did) return;
	try {
		ensureOAuth();
		const session = await getSession(did, { allowStale: true });
		agent = new OAuthUserAgent(session);
		agentDID = did;
		setIdentity();
	} catch {
		sessionStorage.removeItem('forgesi-did');
	}
}

async function signIn(handle) {
	ensureOAuth();
	const authUrl = await createAuthorizationUrl({
		target: { type: 'account', identifier: handle },
		scope: SCOPE,
	});
	await new Promise((r) => setTimeout(r, 300)); // let localStorage persist
	window.location.assign(authUrl);
}

function signOut() {
	if (agentDID) deleteStoredSession(agentDID);
	agent = null;
	agentDID = null;
	sessionStorage.removeItem('forgesi-did');
	setIdentity();
}

function setIdentity() {
	const el = $('identity-btn');
	if (!el) return;
	if (agent) {
		el.textContent = (agentDID || '').replace('did:plc:', '').replace('did:web:', '').slice(0, 18);
		el.className = 'ghost-pill mono';
		el.title = agentDID || '';
	} else {
		el.textContent = 'sign in';
		el.className = 'ghost-pill dim2';
	}
}

// the sign-in modal doubles as the session card. `why` explains the ask.
function openAuth(why) {
	const m = $('auth-modal');
	if (!m) return;
	m.hidden = false;
	$('auth-err').textContent = '';
	$('auth-session').style.display = 'none';
	$('auth-body').style.display = agent ? 'none' : 'flex';
	if (agent) {
		$('auth-session').style.display = 'block';
		$('auth-did').textContent = agentDID;
	} else if (why) {
		$('auth-err').className = 'why';
		$('auth-err').textContent = why;
	}
	if (!agent) $('auth-handle').focus();
}

function closeAuth() { $('auth-modal').hidden = true; }

// gate a write action: resolve when signed in, open the modal otherwise.
function requireSession(why) {
	if (agent) return Promise.resolve();
	openAuth(why || 'sign in to continue');
	return Promise.reject(new Error('auth-required'));
}

// service-auth: mint a JWT at the PDS for the given audience/method,
// then call the knot with it.
async function knotFetch(method, path, body, aud, lxm) {
	const headers = { 'Content-Type': 'application/json' };
	if (agent) {
		const res = await agent.handle('/xrpc/com.atproto.server.getServiceAuth', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ aud, lxm, exp: Math.floor(Date.now() / 1000) + 120 }),
		});
		if (!res.ok) throw new Error('service auth: ' + res.status);
		const { token } = await res.json();
		headers['Authorization'] = 'Bearer ' + token;
	} else {
		// not signed in: local dev accepts the dev bearer; prod 401s.
		headers['Authorization'] = 'Bearer ' + (localStorage.getItem('knot-token') || 'dev');
	}
	const res = await fetch('/xrpc' + path, {
		method, headers,
		body: body === undefined ? undefined : JSON.stringify(body),
	});
	if (!res.ok) {
		const json = await res.json().catch(() => ({}));
		throw Object.assign(new Error(json.message || json.error || res.status), { json, status: res.status });
	}
	return res.json();
}

// ---------- UI ----------

async function xrpc(method, path, body) {
	if (method !== 'GET') {
		return knotFetch(method, path, body, KNOT_DID, null);
	}
	const res = await fetch('/xrpc' + path);
	const json = await res.json().catch(() => ({}));
	if (!res.ok) throw Object.assign(new Error(json.message || json.error || res.status), { json, status: res.status });
	return json;
}

let reposLoaded = false;
async function loadRepos() {
	const sel = $('repo-switch');
	if (!reposLoaded) {
		sel.replaceChildren(Object.assign(document.createElement('option'), { textContent: 'loading…' }));
	}
	try {
		const out = await xrpc('GET', '/org.tangled.repo.listRepos');
		repos = out.repos || [];
		sel.replaceChildren(...repos.map((r) => {
			const el = document.createElement('option');
			el.value = r.did;
			el.textContent = r.name;
			return el;
		}));
		if (current) sel.value = current.did;
		reposLoaded = true;
		if (!current && repos.length) openRepo(repos[0]);
	} catch (e) { log('listRepos: ' + e.message, 'err'); }
}

async function openRepo(r) {
	current = r;
	liveItems = [];
	const sel = $('repo-switch');
	if (sel.value !== r.did) sel.value = r.did;
	$('repo-head').style.display = 'block';
	$('workspace').style.display = 'grid';
	$('repo-title').textContent = r.name;
	$('repo-did').textContent = r.did;
	log('');
	$('plan-list').replaceChildren(skeletonRows(4, true));
	$('feed').replaceChildren(skeletonRows(3));
	loadTodo();
	loadStream();
	loadProposals();
}

// ---------- the plan (todo.md) ----------

function runForTask(title) {
	return runItems.find((r) => r.title === title
		&& ['assigned', 'running', 'queued'].includes(r.status));
}

function failedForTask(title) {
	return runItems.some((r) => r.title === title && r.status === 'failed');
}

function renderPlan() {
	const box = $('plan-list');
	const rows = [];
	const lines = todoText.split('\n');
	let shown = 0;
	for (const raw of lines) {
		const m = raw.match(/^\s*- \[( |x|X)\] (.*)$/);
		if (!m) {
			if (raw.trim()) rows.push(Object.assign(document.createElement('div'), {
				className: 'plan-note', textContent: raw.trim(),
			}));
			continue;
		}
		shown++;
		const done = m[1].toLowerCase() === 'x';
		const text = m[2].trim();
		const running = !done && runForTask(text);
		const failed = !done && !running && failedForTask(text);
		const el = document.createElement('div');
		el.className = 'planrow' + (done ? ' done' : '') + (running ? ' running' : '') + (failed ? ' failed' : '');
		const glyph = document.createElement('span');
		glyph.className = 'glyph';
		if (running) glyph.appendChild(spinEl(true));
		el.appendChild(glyph);
		const t = document.createElement('span');
		t.className = 'pt';
		t.textContent = text;
		if (done) t.className = 'pt done';
		el.appendChild(t);
		if (running && running.branch) {
			const meta = document.createElement('span');
			meta.className = 'pr-meta mono';
			meta.textContent = running.branch;
			el.appendChild(meta);
		}
		rows.push(el);
	}
	box.replaceChildren(...(rows.length ? rows : [emptyEl('empty plan — add the first task below')]));
}

let todoLoading = false;
async function loadTodo() {
	if (!current || todoLoading) return;
	todoLoading = true;
	try {
		const out = await knotFetch('POST', '/org.tangled.repo.getFile', { repo: current.did, path: 'todo.md', ref: 'main' }, current.did, 'org.tangled.repo.getFile');
		todoText = out.content;
		todoHead = out.head;
	} catch {
		todoText = '# todo\n';
		todoHead = null;
	} finally {
		todoLoading = false;
	}
	renderPlan();
}

// append one task line to todo.md — the plan IS the queue.
async function addTask(text) {
	await requireSession('sign in to add to the plan');
	try {
		const fresh = await knotFetch('POST', '/org.tangled.repo.getFile', { repo: current.did, path: 'todo.md', ref: 'main' }, current.did, 'org.tangled.repo.getFile');
		const sep = fresh.content.endsWith('\n') ? '' : '\n';
		const out = await knotFetch('POST', '/org.tangled.repo.putFile', {
			repo: current.did,
			path: 'todo.md',
			content: fresh.content + sep + '- [ ] ' + text + '\n',
			baseHead: fresh.head || undefined,
			msg: 'plan: add "' + text.slice(0, 60) + '"',
		}, current.did, 'org.tangled.repo.putFile');
		todoText = fresh.content + sep + '- [ ] ' + text + '\n';
		todoHead = out.head;
		renderPlan();
		log('added to the plan — the fleet will pick it up', 'ok');
	} catch (e) {
		if (e.message !== 'auth-required') log('could not add: ' + e.message, 'err');
	}
}

// ---------- the stream ----------

function runTs(r) { return Date.parse(r.createdAt) || 0; }

function feedRunEl(r) {
	const el = document.createElement('div');
	el.className = 'feedrow';
	const st = document.createElement('span');
	st.className = 'st ' + (r.status || '');
	if (['assigned', 'running', 'queued'].includes(r.status)) st.appendChild(spinEl(true));
	else st.textContent = r.status;
	el.appendChild(st);
	const t = document.createElement('span');
	t.className = 'fr-title';
	t.textContent = r.title;
	el.appendChild(t);
	const meta = document.createElement('span');
	meta.className = 'meta mono';
	meta.textContent = r.branch || '';
	el.appendChild(meta);
	el.appendChild(Object.assign(document.createElement('span'), { className: 'flex' }));
	const ago = document.createElement('span');
	ago.className = 'ago small';
	ago.textContent = timeago(r.createdAt || r.id);
	el.appendChild(ago);
	return el;
}

function feedCommitEl(item) {
	const el = document.createElement('div');
	el.className = 'feedrow';
	const st = document.createElement('span');
	st.className = 'st done';
	el.appendChild(st);
	const t = document.createElement('span');
	t.className = 'fr-title mono';
	t.textContent = item.text;
	el.appendChild(t);
	el.appendChild(Object.assign(document.createElement('span'), { className: 'flex' }));
	const ago = document.createElement('span');
	ago.className = 'ago small';
	ago.textContent = 'now';
	el.appendChild(ago);
	return el;
}

async function feedProposalEl(rec) {
	const v = rec.value || {};
	const rkey = (rec.uri || '').split('/').pop();
	const el = document.createElement('article');
	el.className = 'ticket';
	const title = (v.title && v.title.text) || '(untitled)';
	const closed = v.state === 'closed' || v.state === 'merged';
	let verdictText = null;
	try {
		const out = await xrpc('GET', `/com.atproto.repo.listRecords?repo=${encodeURIComponent(current.did)}&collection=org.tangled.feed.comment&limit=100`);
		const hit = (out.records || []).find((c) => {
			const subj = (c.value || {}).subject || {};
			return subj.uri && subj.uri.endsWith('/' + rkey);
		});
		if (hit) verdictText = ((hit.value || {}).body || {}).text || null;
	} catch { /* none */ }

	el.innerHTML = `<span class="st pull">proposal</span>
		<div class="ticket-body"><div class="tt"></div>
		<div class="meta mono"></div>
		${verdictText ? `<div class="verdict"><span class="who">machine review</span><span class="vt"></span></div>` : ''}
		<div class="acts"></div></div>`;
	el.querySelector('.tt').textContent = title;
	el.querySelector('.meta').textContent = `${v.targetBranch || ''} → main`;
	if (verdictText) el.querySelector('.vt').textContent = verdictText;
	const acts = el.querySelector('.acts');
	if (closed) {
		acts.appendChild(Object.assign(document.createElement('span'), { className: 'pstate', textContent: v.state }));
	} else if (!agent) {
		const b = document.createElement('button');
		b.className = 'act merge';
		b.textContent = 'merge';
		b.onclick = () => openAuth('sign in to merge');
		acts.appendChild(b);
		acts.appendChild(Object.assign(document.createElement('span'), { className: 'pstate', textContent: 'or browse only' }));
	} else {
		const mb = document.createElement('button');
		mb.className = 'act merge';
		mb.textContent = 'merge';
		const cb = document.createElement('button');
		cb.className = 'act';
		cb.textContent = 'close';
		const stateEl = Object.assign(document.createElement('span'), { className: 'pstate' });
		acts.append(mb, cb, stateEl);
		mb.onclick = () => busy(mb, async () => {
			stateEl.textContent = 'merging…';
			try {
				const out = await knotFetch('POST', '/org.tangled.git.merge', {
					repo: current.did, base: 'main', head: v.targetBranch,
				}, current.did, 'org.tangled.git.merge');
				stateEl.textContent = out.fastForward ? 'fast-forwarded' : 'merged ' + out.commit.slice(0, 8);
				try {
					await knotFetch('POST', '/com.atproto.repo.createRecord', {
						repo: current.did,
						collection: 'org.tangled.feed.comment',
						record: {
							$type: 'org.tangled.feed.comment',
							subject: { uri: rec.uri, cid: rec.cid },
							body: { $type: 'org.tangled.markup.markdown', text: 'merged by ' + agentDID },
							createdAt: new Date().toISOString(),
						},
					}, current.did, 'com.atproto.repo.createRecord');
				} catch { /* comment is best-effort */ }
				openRepo(current);
			} catch (e) {
				stateEl.textContent = 'failed: ' + e.message;
			}
		});
		cb.onclick = () => busy(cb, async () => {
			stateEl.textContent = 'closing…';
			try {
				await knotFetch('POST', '/com.atproto.repo.putRecord', {
					repo: current.did, collection: 'org.tangled.track.ticket', rkey,
					record: { ...v, state: 'closed' },
				}, current.did, 'com.atproto.repo.putRecord');
				stateEl.textContent = 'closed';
				openRepo(current);
			} catch (e) {
				stateEl.textContent = 'failed: ' + e.message;
			}
		});
	}
	return { ts: Date.parse(v.createdAt) || 0, el };
}

async function loadStream() {
	if (!current) return;
	try {
		const out = await xrpc('GET', '/org.tangled.fleet.runs?repo=' + encodeURIComponent(current.did));
		runItems = (out.runs || []).slice(0, 20);
	} catch { runItems = []; }
	renderPlan();
	renderStream();
}

async function renderStream() {
	const box = $('feed');
	const items = [];
	for (const p of proposalRecs) items.push(await feedProposalEl(p));
	for (const r of runItems) items.push({ ts: runTs(r), el: feedRunEl(r) });
	for (const c of liveItems) items.push({ ts: c.ts, el: feedCommitEl(c) });
	items.sort((a, b) => b.ts - a.ts);
	box.replaceChildren(...(items.length ? items.map((i) => i.el) : [emptyEl('the fleet is idle — add a task to the plan')]));
}

async function loadProposals() {
	if (!current) return;
	try {
		const out = await xrpc('GET', `/com.atproto.repo.listRecords?repo=${encodeURIComponent(current.did)}&collection=org.tangled.track.ticket&limit=50`);
		const records = out.records || [];
		proposalRecs = records.filter((rec) => (rec.value || {}).targetBranch);
	} catch { proposalRecs = []; }
	renderStream();
}

// ---------- live firehose (HTTP poll; WS frames are binary dag-cbor) ----------

let liveCursor = 0, liveFail = 0;
async function pollLive() {
	if (document.hidden || !$('feed')) return;
	try {
		const out = await xrpc('GET', '/com.atproto.sync.getEvents?cursor=' + liveCursor + '&limit=25');
		const events = out.events || [];
		for (const ev of events) {
			liveCursor = Math.max(liveCursor, ev.seq || 0);
			liveItems.unshift({
				ts: Date.now(),
				text: shortRepo(ev.repo) + ' ' + (ev.kind || 'commit') + (ev.change ? ' ' + String(ev.change).slice(0, 7) : ''),
			});
		}
		if (events.length) {
			liveItems = liveItems.slice(0, 12);
			renderStream();
		}
		$('live-dot').className = 'dot on';
		liveFail = 0;
	} catch {
		if (++liveFail > 3) $('live-dot').className = 'dot';
	}
}

function scheduleLiveRetry() {
	setInterval(() => { pollLive().catch(() => {}); }, 2000);
}

function shortRepo(did) {
	if (!did) return '';
	return (did.split(':repo:')[1] || did).slice(0, 18);
}

// ---------- console wiring (the callback page has none of this DOM) ----------

const isConsole = !!document.getElementById('repo-switch');

if (isConsole) $('create-repo').onclick = () => busy($('create-repo'), async () => {
	const name = $('new-repo').value.trim();
	if (!name) return;
	try {
		await requireSession('sign in to create a repo');
		const out = await knotFetch('POST', '/org.tangled.repo.createRepo', { name, defaultBranch: 'main' }, KNOT_DID, 'org.tangled.repo.createRepo');
		$('new-repo').value = '';
		$('new-repo-row').style.display = 'none';
		log('repo created: ' + out.did, 'ok');
		await loadRepos();
		const r = repos.find((x) => x.did === out.did);
		openRepo(r || { did: out.did, name });
	} catch (e) { if (e.message !== 'auth-required') log(e.message, 'err'); }
});

if (isConsole) {
	const add = () => {
		const text = $('plan-input').value.trim();
		if (!text || !current) return;
		$('plan-input').value = '';
		addTask(text);
	};
	$('plan-add').onclick = add;
	$('plan-input').onkeydown = (e) => { if (e.key === 'Enter') add(); };
	$('edit-todo').onclick = () => {
		if (!current) return;
		$('todo-editor').value = todoText;
		$('todo-status').textContent = todoHead ? 'last commit ' + String(todoHead).slice(0, 8) : 'new file';
		$('todo-modal').hidden = false;
		$('todo-editor').focus();
	};
	$('save-todo').onclick = () => busy($('save-todo'), async () => {
		await requireSession('sign in to edit todo.md');
		$('todo-status').replaceChildren(spinEl(true), Object.assign(document.createElement('span'), { textContent: ' saving' }));
		try {
			const out = await knotFetch('POST', '/org.tangled.repo.putFile', {
				repo: current.did,
				path: 'todo.md',
				content: $('todo-editor').value,
				baseHead: todoHead || undefined,
				msg: 'Update todo.md from console',
			}, current.did, 'org.tangled.repo.putFile');
			todoText = $('todo-editor').value;
			todoHead = out.head;
			$('todo-status').textContent = 'saved ' + out.commit.slice(0, 8);
			renderPlan();
			log('todo.md saved', 'ok');
			setTimeout(() => { $('todo-modal').hidden = true; }, 500);
		} catch (e) {
			if (e.status === 409) {
				$('todo-status').textContent = 'conflict — todo.md changed, reloading';
				const fresh = await knotFetch('POST', '/org.tangled.repo.getFile', { repo: current.did, path: 'todo.md', ref: 'main' }, current.did, 'org.tangled.repo.getFile');
				todoText = fresh.content; todoHead = fresh.head;
				$('todo-editor').value = fresh.content;
				renderPlan();
			} else {
				$('todo-status').textContent = 'failed: ' + e.message;
			}
		}
	});
	$('identity-btn').onclick = () => openAuth();
	$('auth-cancel').onclick = closeAuth;
	$('auth-go').onclick = () => busy($('auth-go'), async () => {
		const handle = $('auth-handle').value.trim();
		if (!handle) return;
		try {
			await signIn(handle);
		} catch (err) {
			$('auth-err').textContent = (err.message || err);
		}
	});
	$('auth-handle').onkeydown = (e) => { if (e.key === 'Enter') $('auth-go').click(); };
	$('auth-out').onclick = () => { signOut(); closeAuth(); };
	// close overlays on backdrop click
	for (const id of ['auth-modal', 'todo-modal']) {
		$(id).addEventListener('click', (e) => { if (e.target.id === id) $(id).hidden = true; });
	}
}

// ---------- boot ----------

// OAuth callback: the authorization server redirects with params in the
// hash (not search), finalize then return to the console. This page has
// almost no DOM — nothing else in this module may touch it.
function callbackBoot() {
	const statusEl = document.getElementById('status');
	const params = new URLSearchParams(location.hash.slice(1));
	history.replaceState(null, '', location.pathname);
	ensureOAuth();
	finalizeAuthorization(params)
		.then(({ session }) => {
			if (statusEl) statusEl.textContent = 'signed in as ' + session.info.sub + ' — returning…';
			sessionStorage.setItem('forgesi-did', session.info.sub);
			location.replace('/app');
		})
		.catch((e) => {
			if (statusEl) statusEl.textContent = 'sign-in failed: ' + (e.message || e);
			else console.error('sign-in failed', e);
		});
}

function consoleBoot() {
	const sel = $('repo-switch');
	sel.onchange = () => {
		const r = repos.find((x) => x.did === sel.value);
		if (r) openRepo(r);
	};
	const row = $('new-repo-row');
	$('new-repo-btn').onclick = () => {
		row.style.display = row.style.display === 'none' ? 'flex' : 'none';
		if (row.style.display === 'flex') $('new-repo').focus();
	};
	resumeSession().then(() => {
		loadRepos();
		scheduleLiveRetry();
	});
}

// ---------- boot dispatch ----------

if (location.pathname === '/oauth/callback/') {
	callbackBoot();
} else {
	consoleBoot();
}
