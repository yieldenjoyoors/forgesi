// ForgeSI fleet runner worker: proxies run requests into the container.
// The agent binary inside listens on 8080 (POST /run = one agent loop).
//
// This is the manual container-DO pattern: the class starts the image
// explicitly (RUNNER_IMAGE var — built and pushed by CI) because the
// @cloudflare/containers start path fails with an opaque platform error
// on this account. The old forge sandbox ran this exact pattern for
// hours without a hitch.
import { DurableObject } from "cloudflare:workers";

interface Env {
	FLEET_AGENT_DO: DurableObjectNamespace<FleetAgent>;
	RUNNER_IMAGE?: string;
	KNOT_RUNNER_KEY?: string;
}

export class FleetAgent extends DurableObject<Env> {
	async fetch(req: Request): Promise<Response> {
		const container = this.ctx.container;
		if (!container) {
			return new Response("no container bound to this DO\n", { status: 500 });
		}
		if (!container.running) {
			container.start({
				image: this.env.RUNNER_IMAGE,
				enableInternet: true,
			});
		}
		const port = container.getTcpPort(8080);
		let lastError: unknown;
		for (let i = 0; i < 120; i++) {
			try {
				const res = await port.fetch("http://container/health", {
					signal: AbortSignal.timeout(1000),
				});
				await res.body?.cancel();
				if (res.ok) {
					lastError = undefined;
					break;
				}
				lastError = new Error("health check " + res.status);
			} catch (e) {
				lastError = e;
			}
			await scheduler.wait(500);
		}
		if (lastError) {
			return new Response("container not ready: " + String(lastError) + "\n", { status: 502 });
		}
		const url = new URL(req.url);
		url.protocol = "http:";
		url.host = "container";
		const forwarded = new Request(url, req);
		forwarded.headers.delete("host");
		return port.fetch(forwarded);
	}
}

export default {
	async fetch(req: Request, env: Env): Promise<Response> {
		const url = new URL(req.url);
		if (req.method === "POST" && url.pathname === "/run") {
			if (env.KNOT_RUNNER_KEY && req.headers.get("Authorization") !== "Bearer " + env.KNOT_RUNNER_KEY) {
				return new Response("runner key required\n", { status: 401 });
			}
			const container = env.FLEET_AGENT_DO.getByName("fleet");
			return container.fetch(req);
		}
		if (url.pathname === "/health") {
			return new Response("runner ok");
		}
		return new Response("forgeSI fleet runner\n", { status: 200 });
	},
};
