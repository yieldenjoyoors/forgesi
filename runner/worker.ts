// ForgeSI fleet runner worker: proxies run requests into the container.
// The agent binary inside listens on 8080 (POST /run = one agent loop).
import { Container } from "cloudflare:workers";

export class FleetAgent extends Container {
	defaultPort = 8080;
}

export default {
	async fetch(req: Request, env: { FLEET_AGENT_DO: { getByName: (n: string) => { fetch: (r: Request) => Promise<Response> } }; KNOT_RUNNER_KEY?: string }): Promise<Response> {
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
