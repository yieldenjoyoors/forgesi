// The OAuth callback module: deliberately tiny. It configures the OAuth
// client, finalizes the authorization response (hash params), and returns
// to the console. Every step updates the status line and the whole thing
// is under a hard timeout — no silent hangs.
import {
	configureOAuth,
	finalizeAuthorization,
} from '@atcute/oauth-browser-client';
import {
	CompositeDidDocumentResolver,
	LocalActorResolver,
	PlcDidDocumentResolver,
	WebDidDocumentResolver,
	XrpcDidDocumentResolver,
	XrpcHandleResolver,
} from '@atcute/identity-resolver';

const statusEl = document.getElementById('status');
const set = (t) => { if (statusEl) statusEl.textContent = t; console.log('[callback]', t); };
const setSpin = (t) => { if (statusEl) statusEl.innerHTML = '<span class="spin"></span>' + t; };

const didDocumentResolver = new CompositeDidDocumentResolver({
	methods: {
		plc: new PlcDidDocumentResolver(),
		web: new WebDidDocumentResolver(),
		xrpc: new XrpcDidDocumentResolver({ serviceUrl: 'https://public.api.bsky.app' }),
	},
});

function configure() {
	configureOAuth({
		metadata: {
			client_id: 'https://forgesi.org/oauth/client-metadata.json',
			redirect_uri: 'https://forgesi.org/oauth/callback/',
		},
		identityResolver: new LocalActorResolver({
			handleResolver: new XrpcHandleResolver({ serviceUrl: 'https://public.api.bsky.app' }),
			didDocumentResolver,
		}),
	});
}

async function boot() {
	const params = new URLSearchParams(location.hash.slice(1));
	if (!params.get('code') && !params.get('error')) {
		set('no authorization response — start again from the console');
		return;
	}
	if (params.get('error')) {
		set('authorization failed: ' + (params.get('error_description') || params.get('error')));
		return;
	}

	history.replaceState(null, '', location.pathname);

	setSpin('resolving your PDS…');
	try {
		configure();
	} catch (e) {
		set('client init failed: ' + (e.message || e));
		return;
	}

	setSpin('exchanging the code…');
	const timeout = new Promise((_, rej) =>
		setTimeout(() => rej(new Error('timed out after 45s — close this and sign in again')), 45000));
	try {
		const { session } = await Promise.race([finalizeAuthorization(params), timeout]);
		set('signed in as ' + session.info.sub + ' — returning to the console…');
		sessionStorage.setItem('forgesi-did', session.info.sub);
		setTimeout(() => location.replace('/app'), 800);
	} catch (e) {
		set('sign-in failed: ' + (e.message || e) + ' — try again from the console');
	}
}

boot();
