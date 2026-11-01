import { run } from "./core.mjs";
import {
  createRuntimeContext,
  WorkflowEntrypointBase,
  WorkerEntrypointBase,
} from "./runtime.mjs";

async function fetch(req, env, ctx) {
  const binding = {};
  await run(createRuntimeContext({ env, ctx, binding }));
  return binding.handleRequest(req);
}

async function scheduled(event, env, ctx) {
  const binding = {};
  await run(createRuntimeContext({ env, ctx, binding }));
  return binding.runScheduler(event);
}

async function queue(batch, env, ctx) {
  const binding = {};
  await run(createRuntimeContext({ env, ctx, binding }));
  return binding.handleQueueMessageBatch(batch);
}

async function email(message, env, ctx) {
  const binding = {};
  await run(createRuntimeContext({ env, ctx, binding }));
  return binding.handleEmail(message);
}

// tail handles a batch of trace events for a Worker configured as a
// tail_consumers entry of another ("producer") Worker.
async function tail(events, env, ctx) {
  const binding = {};
  await run(createRuntimeContext({ env, ctx, binding }));
  return binding.handleTail(events);
}

// onRequest handles request to Cloudflare Pages
async function onRequest(ctx) {
  const binding = {};
  const { request, env } = ctx;
  await run(createRuntimeContext({ env, ctx, binding }));
  return binding.handleRequest(request);
}

export default {
  fetch,
  scheduled,
  queue,
  email,
  tail,
  onRequest,
};

// GoDurableObject is the base class a Go-hosted Durable Object subclasses.
// Unlike fetch/scheduled/queue/email above (one wasm instance per trigger
// invocation), a Durable Object instance's whole point is that state (Go
// memory, in-flight goroutines, ...) survives across multiple triggers
// (fetch/alarm/webSocket*) delivered to the same object, so #bind() runs the
// wasm instance's `run()` at most once (memoized in `this.ready`) and reuses
// it for every subsequent trigger.
//
// A subclass is generated per Durable Object class name by
// cmd/workers-assets-gen's -durable-objects flag, e.g.:
//   export class Counter extends GoDurableObject { static goClassName = "Counter"; }
export class GoDurableObject {
  // goClassName identifies this class to the Go side (durableobjects.Register's
  // className argument), via the "durableObject" runtime context entry.
  // A generated subclass sets it explicitly; it falls back to the JS class
  // name (this.constructor.name) if unset.
  static goClassName = undefined;

  constructor(state, env) {
    this.state = state;
    this.env = env;
    this.binding = undefined;
    this.ready = undefined;
  }

  async #bind() {
    if (!this.ready) {
      const binding = {};
      this.binding = binding;
      this.ready = run(
        createRuntimeContext({
          env: this.env,
          ctx: this.state,
          binding,
          durableObject: { className: this.constructor.goClassName ?? this.constructor.name },
        }),
      );
    }
    await this.ready;
    return this.binding;
  }

  async fetch(req) {
    return (await this.#bind()).handleDurableObjectFetch(req);
  }

  async alarm(info) {
    return (await this.#bind()).handleDurableObjectAlarm(info);
  }

  async webSocketMessage(ws, message) {
    return (await this.#bind()).handleDurableObjectWebSocketMessage(ws, message);
  }

  async webSocketClose(ws, code, reason, wasClean) {
    return (await this.#bind()).handleDurableObjectWebSocketClose(ws, code, reason, wasClean);
  }

  async webSocketError(ws, error) {
    return (await this.#bind()).handleDurableObjectWebSocketError(ws, error);
  }
}

// GoWorkflowEntrypoint is the base class a Go-hosted Workflow's
// WorkflowEntrypoint subclasses (a subclass is mandatory: unlike a Durable
// Object, Workflows requires extending cloudflare:workers'
// WorkflowEntrypoint). Each run() trigger runs in its own wasm instance —
// there's no Durable-Object-style cross-trigger state to keep alive — but
// #bind() still memoizes run() (in #ready) per class instance, matching
// GoDurableObject's shape above; Workflows may reuse the same
// WorkflowEntrypoint instance across retries/replays of one run().
//
// A subclass is generated per Workflow class name by
// cmd/workers-assets-gen's -workflows flag, e.g.:
//   export class MyWorkflow extends GoWorkflowEntrypoint { static goClassName = "MyWorkflow"; }
export class GoWorkflowEntrypoint extends WorkflowEntrypointBase {
  // goClassName identifies this class to the Go side (workflows.Register's
  // className argument), via the "workflow" runtime context entry. A
  // generated subclass sets it explicitly; it falls back to the JS class
  // name (this.constructor.name) if unset.
  static goClassName = undefined;

  // Declared (not assigned) so the implicit derived constructor — this
  // class defines none of its own — can initialize them after the required
  // super(ctx, env) call, which the base WorkflowEntrypoint class uses to
  // set the protected `this.ctx`/`this.env` #bind() reads below.
  #binding;
  #ready;

  async #bind() {
    if (!this.#ready) {
      const binding = {};
      this.#binding = binding;
      this.#ready = run(
        createRuntimeContext({
          env: this.env,
          ctx: this.ctx,
          binding,
          workflow: { className: this.constructor.goClassName ?? this.constructor.name },
        }),
      );
    }
    await this.#ready;
    return this.#binding;
  }

  async run(event, step) {
    return (await this.#bind()).handleWorkflowRun(event, step);
  }
}

// GoWorkerEntrypoint is the base class a Go-hosted WorkerEntrypoint (a
// named entrypoint exposing RPC methods, reached e.g. over a Service
// binding, plus its own fetch() trigger) subclasses. It follows the same
// #bind-memoization shape as GoDurableObject/GoWorkflowEntrypoint above,
// but exposes the bind method as `_bind()` (public by convention, not
// truly private) instead of a JS private `#bind`: a generated subclass
// defines its own RPC methods (one per name in -entrypoints=Name:method,...)
// that must call back into it, and a JS private method declared in this
// class body is only reachable from methods defined literally in *this*
// class — not from a subclass's own methods — so it can't stay private.
//
// A subclass is generated per WorkerEntrypoint class name by
// cmd/workers-assets-gen's -entrypoints flag, e.g. for
// "-entrypoints=MyService:add,greet":
//   export class MyService extends GoWorkerEntrypoint {
//     static goClassName = "MyService";
//     async add(...args) { return (await this._bind()).handleRPC("add", args); }
//     async greet(...args) { return (await this._bind()).handleRPC("greet", args); }
//     async fetch(req) { return (await this._bind()).handleEntrypointFetch(req); }
//   }
export class GoWorkerEntrypoint extends WorkerEntrypointBase {
  // goClassName identifies this class to the Go side (rpc.Register's /
  // rpc.RegisterFetch's className argument), via the "entrypoint" runtime
  // context entry. A generated subclass sets it explicitly; it falls back
  // to the JS class name (this.constructor.name) if unset.
  static goClassName = undefined;

  #binding;
  #ready;

  async _bind() {
    if (!this.#ready) {
      const binding = {};
      this.#binding = binding;
      this.#ready = run(
        createRuntimeContext({
          env: this.env,
          ctx: this.ctx,
          binding,
          entrypoint: { className: this.constructor.goClassName ?? this.constructor.name },
        }),
      );
    }
    await this.#ready;
    return this.#binding;
  }
}
