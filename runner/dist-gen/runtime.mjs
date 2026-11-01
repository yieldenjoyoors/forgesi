import { connect } from "cloudflare:sockets";
import { EmailMessage } from "cloudflare:email";
import { NonRetryableError } from "cloudflare:workflows";
import {
  WorkflowEntrypoint as WorkflowEntrypointBase,
  WorkerEntrypoint as WorkerEntrypointBase,
} from "cloudflare:workers";
import mod from "./app.wasm";

export { WorkflowEntrypointBase, WorkerEntrypointBase };

export async function loadModule() {
  return mod;
}

export function createRuntimeContext({
  env,
  ctx,
  binding,
  durableObject,
  workflow,
  entrypoint,
}) {
  return {
    env,
    ctx,
    connect,
    EmailMessage,
    NonRetryableError,
    binding,
    durableObject,
    workflow,
    entrypoint,
  };
}
