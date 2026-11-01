// ForgeSI fleet runner worker: proxies run requests into the container.
// The agent binary inside listens on 8080 (POST /run = one agent loop).
import { Container } from "cloudflare:workers";

export class FleetAgent extends Container {
	defaultPort = 8080;
}

export default {
	async fetch(req: Request, env: { FLEET_AGENTDO: { getByName: (n: string) => { fetch: (r: Request) => Promise<Response> } } }): Promise<Response> {
		const url = new URL(req.url);
		if (req.method === "POST" && url.pathname === "/run") {
			const container = env.FLEET_AGENTDO.getByName("fleet");
			return container.fetch(req);
		}
		if (url.pathname === "/health") {
			return new Response("runner ok");
		}
		return new Response("forgeSI fleet runner\n", { status: 200 });
	},
};
