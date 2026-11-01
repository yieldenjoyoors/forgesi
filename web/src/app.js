// ForgeSI console: the deck. Reads are anonymous; writes require an
// atproto OAuth session (sign in with your PDS handle). Authenticated
// calls mint a short-lived service-auth JWT from your PDS (aud = the
// repo DID or the knot DID, lxm = the method) — exactly what the Tangled
// web app does when talking to knots.
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

function log(msg, cls) {
	const f = $('flash');
	if (f) { f.textContent = msg || ''; f.className = 'flash ' + (cls || ''); }
	console.log('[console]', msg);
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
		setIdentity(did);
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
	setIdentity(null);
}

function setIdentity(did) {
	const el = $('identity');
	if (!el) return;
	el.textContent = did ? 'signed in: ' + did : 'not signed in (reads only)';
	el.className = 'mono ' + (did ? 'ok-flash' : 'dim');
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

async function loadRuns() {
	try {
		const out = await xrpc('GET', '/org.tangled.fleet.runs' + (current ? '?repo=' + encodeURIComponent(current.did) : ''));
		const runs = out.runs || [];
		$('runs').replaceChildren(...(runs.length ? runs.map((r) => {
			const el = document.createElement('div');
			el.className = 'run';
			const st = document.createElement('span');
			st.className = 'st ' + r.status;
			st.textContent = r.status;
			el.appendChild(st);
			el.appendChild(document.createTextNode(' ' + r.title.slice(0, 34)));
			return el;
		}) : [Object.assign(document.createElement('div'), { className: 'empty', textContent: 'no runs yet' })]));
	} catch { /* best effort */ }
}

async function loadRepos() {
	try {
		const out = await xrpc('GET', '/org.tangled.repo.listRepos');
		repos = out.repos || [];
		$('repos').replaceChildren(...repos.map((r) => {
			const el = document.createElement('div');
			el.className = 'repoitem' + (current && current.did === r.did ? ' on' : '');
			el.textContent = r.name;
			el.onclick = () => openRepo(r);
			return el;
		}));
	} catch (e) { log('listRepos: ' + e.message, 'err'); }
}

async function openRepo(r) {
	current = r;
	$('crumb-repo').textContent = r.name;
	$('repo-title').textContent = r.name;
	$('repo-did').textContent = r.did;
	$('ticket-form').style.display = 'flex';
	$('tabs').style.display = 'flex';
	log('');
	loadRepos();
	loadRuns();
	loadTodo();
	try {
		const out = await xrpc('GET', `/com.atproto.repo.listRecords?repo=${encodeURIComponent(r.did)}&collection=org.tangled.track.ticket&limit=50`);
		const records = out.records || [];
		const todos = records.filter((rec) => !(rec.value || {}).targetBranch);
		const proposals = records.filter((rec) => (rec.value || {}).targetBranch);
		$('tickets').replaceChildren(...(todos.length ? todos.map(renderTicket) : [emptyEl('No tickets. Open the first one below.')]));
		renderProposals(r, proposals);
	} catch (e) {
		$('tickets').replaceChildren(emptyEl('listRecords: ' + e.message));
	}
}

// ---------- tabs ----------

document.querySelectorAll('.tab').forEach((btn) => {
	btn.onclick = () => {
		document.querySelectorAll('.tab').forEach((b) => b.classList.toggle('on', b === btn));
		for (const t of ['tickets', 'proposals', 'todo']) {
			$('tab-' + t).style.display = btn.dataset.tab === t ? 'block' : 'none';
		}
		if (btn.dataset.tab === 'todo' && current) loadTodo();
	};
});

// ---------- todo.md editor ----------

let todoHead = null;
async function loadTodo() {
	if (!current) return;
	$('todo-status').textContent = 'loading…';
	try {
		const out = await knotFetch('POST', '/org.tangled.repo.getFile', { repo: current.did, path: 'todo.md', ref: 'main' }, current.did, 'org.tangled.repo.getFile');
		$('todo-editor').value = out.content;
		todoHead = out.head;
		$('todo-status').textContent = (out.content.match(/^- \[[ xX]\]/gm) || []).length + ' items';
	} catch {
		$('todo-editor').value = '# todo\n\n- [ ] first task\n';
		todoHead = null;
		$('todo-status').textContent = 'new file';
	}
}

$('save-todo').onclick = async () => {
	if (!current) return;
	$('save-todo').disabled = true;
	$('todo-status').textContent = 'saving…';
	try {
		const out = await knotFetch('POST', '/org.tangled.repo.putFile', {
			repo: current.did,
			path: 'todo.md',
			content: $('todo-editor').value,
			baseHead: todoHead || undefined,
			msg: 'Update todo.md from console',
		}, current.did, 'org.tangled.repo.putFile');
		todoHead = out.head;
		$('todo-status').textContent = 'saved ' + out.commit.slice(0, 8);
		log('todo.md saved', 'ok');
	} catch (e) {
		if (e.status === 409) {
			log('todo.md changed underneath you — reloading the fresh file', 'err');
			loadTodo();
		} else {
			log('save failed: ' + e.message, 'err');
		}
	} finally {
		$('save-todo').disabled = false;
	}
};

function renderTicket(rec) {
	const v = rec.value || {};
	const title = (v.title && v.title.text) || '(untitled)';
	const el = document.createElement('div');
	el.className = 'ticket';
	const state = v.state || 'open';
	el.innerHTML = `<span class="st ${state}">${state}</span><div><div class="tt"></div></div>`;
	el.querySelector('.tt').textContent = title;
	return el;
}

function emptyEl(text) {
	return Object.assign(document.createElement('div'), { className: 'empty', textContent: text });
}

async function renderProposals(repo, proposals) {
	const box = $('proposals');
	if (!proposals.length) {
		box.replaceChildren(emptyEl('No proposals yet.'));
		return;
	}
	let comments = [];
	try {
		const out = await xrpc('GET', `/com.atproto.repo.listRecords?repo=${encodeURIComponent(repo.did)}&collection=org.tangled.feed.comment&limit=100`);
		comments = out.records || [];
	} catch { /* none */ }
	box.replaceChildren(...proposals.map((rec) => {
		const v = rec.value || {};
		const rkey = (rec.uri || '').split('/').pop();
		const el = document.createElement('div');
		el.className = 'ticket';
		const title = (v.title && v.title.text) || '(untitled)';
		const verdict = comments.find((c) => {
			const subj = (c.value || {}).subject || {};
			return subj.uri && subj.uri.endsWith('/' + rkey);
		});
		el.innerHTML = `<span class="st pull">proposal</span>
			<div style="min-width:0"><div class="tt"></div>
			<div class="meta mono">${v.targetBranch || ''}</div>
			${verdict ? `<div class="verdict"><span class="who">machine review</span><br><span class="vt"></span></div>` : ''}
			<div class="acts">
				<button class="act merge">merge</button>
				<button class="act close">close</button>
				<span class="meta mono pstate"></span>
			</div></div>`;
		el.querySelector('.tt').textContent = title;
		if (verdict) el.querySelector('.vt').textContent = ((verdict.value || {}).body || {}).text || '';
		const stateEl = el.querySelector('.pstate');
		if (!agent) {
			stateEl.textContent = 'sign in to act';
		} else {
			el.querySelector('.merge').onclick = async () => {
				stateEl.textContent = 'merging…';
				try {
					const out = await knotFetch('POST', '/org.tangled.git.merge', {
						repo: repo.did, base: 'main', head: v.targetBranch,
					}, repo.did, 'org.tangled.git.merge');
					stateEl.textContent = out.fastForward ? 'fast-forwarded' : 'merged ' + out.commit.slice(0, 8);
					// mark the ticket closed with a merge comment
					try {
						await knotFetch('POST', '/com.atproto.repo.createRecord', {
							repo: repo.did,
							collection: 'org.tangled.feed.comment',
							record: {
								$type: 'org.tangled.feed.comment',
								subject: { uri: rec.uri, cid: rec.cid },
								body: { $type: 'org.tangled.markup.markdown', text: 'merged by ' + agentDID },
								createdAt: new Date().toISOString(),
							},
						}, repo.did, 'com.atproto.repo.createRecord');
					} catch { /* comment is best-effort */ }
					openRepo(repo);
				} catch (e) {
					stateEl.textContent = 'failed: ' + e.message;
				}
			};
			el.querySelector('.close').onclick = async () => {
				stateEl.textContent = 'closing…';
				try {
					await knotFetch('POST', '/com.atproto.repo.putRecord', {
						repo: repo.did, collection: 'org.tangled.track.ticket', rkey,
						record: { ...v, state: 'closed' },
					}, repo.did, 'com.atproto.repo.putRecord');
					stateEl.textContent = 'closed';
					openRepo(repo);
				} catch (e) {
					stateEl.textContent = 'failed: ' + e.message;
				}
			};
		}
		return el;
	}));
}

// ---------- live firehose (HTTP poll; WS frames are binary dag-cbor) ----------

let liveCursor = 0, liveFail = 0;
async function pollLive() {
	if (document.hidden || !$('live')) return;
	try {
		const out = await xrpc('GET', '/com.atproto.sync.getEvents?cursor=' + liveCursor + '&limit=25');
		const events = out.events || [];
		for (const ev of events) {
			liveCursor = Math.max(liveCursor, ev.seq || 0);
			const box = $('live');
			if (!box) break;
			const el = document.createElement('div');
			el.className = 'run';
			el.innerHTML = `<span class="st done">•</span> <span class="mono" style="font-size:11px"></span>`;
			el.querySelector('.mono').textContent =
				shortRepo(ev.repo) + ' ' + (ev.kind || 'commit') +
				(ev.change ? ' ' + String(ev.change).slice(0, 7) : '');
			el.title = (ev.repo || '') + ' @' + (ev.rev || '');
			box.prepend(el);
			while (box.children.length > 14) box.lastChild.remove();
		}
		$('live-dot').className = 'dot on';
		liveFail = 0;
	} catch {
		if (++liveFail > 3) {
			$('live-dot').className = 'dot';
		}
	}
}

function scheduleLiveRetry() {
	setInterval(() => { pollLive().catch(() => {}); }, 2000);
}

function shortRepo(did) {
	if (!did) return '';
	return (did.split(':repo:')[1] || did).slice(0, 18);
}

$('create-repo').onclick = async () => {
	const name = $('new-repo').value.trim();
	if (!name) return;
	try {
		const out = await knotFetch('POST', '/org.tangled.repo.createRepo', { name, defaultBranch: 'main' }, KNOT_DID, 'org.tangled.repo.createRepo');
		$('new-repo').value = '';
		log('repo created: ' + out.did, 'ok');
		await loadRepos();
		const r = repos.find((x) => x.did === out.did);
		openRepo(r || { did: out.did, name });
	} catch (e) { log(e.message, 'err'); }
};

$('create-ticket').onclick = async () => {
	const text = $('new-ticket').value.trim();
	if (!text || !current) return;
	try {
		await knotFetch('POST', '/com.atproto.repo.createRecord', {
			repo: current.did,
			collection: 'org.tangled.track.ticket',
			record: {
				$type: 'org.tangled.track.ticket',
				title: { $type: 'org.tangled.markup.markdown#inline', text },
				state: 'open',
				createdAt: new Date().toISOString(),
			},
		}, current.did, 'com.atproto.repo.createRecord');
		$('new-ticket').value = '';
		log('ticket opened', 'ok');
		openRepo(current);
	} catch (e) { log(e.message, 'err'); }
};

// ---------- sign-in form ----------

document.getElementById('signin').onsubmit = async (e) => {
	e.preventDefault();
	const handle = document.getElementById('handle').value.trim();
	if (!handle) return;
	log('starting sign-in for ' + handle + '…');
	try {
		await signIn(handle);
	} catch (err) {
		log('sign-in failed: ' + (err.message || err), 'err');
	}
};

// ---------- boot ----------

// OAuth callback: the authorization server redirects with params in the
// hash (not search), finalize then return to the console.
if (location.pathname === '/oauth/callback/') {
	$('status').textContent = 'finalizing sign-in…';
	const params = new URLSearchParams(location.hash.slice(1));
	history.replaceState(null, '', '/app');
	ensureOAuth();
	finalizeAuthorization(params)
		.then(({ session }) => {
			sessionStorage.setItem('forgesi-did', session.info.sub);
			location.replace('/app');
		})
		.catch((e) => {
			$('status').textContent = 'sign-in failed: ' + (e.message || e);
		});
} else if (document.getElementById('repos')) {
	resumeSession().then(() => { loadRepos(); loadRuns(); scheduleLiveRetry(); });
}
