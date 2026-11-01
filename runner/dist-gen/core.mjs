import "./wasm_exec.js";
import { loadModule } from "./runtime.mjs";

let mod;

globalThis.tryCatch = (fn) => {
  try {
    return {
      result: fn(),
    };
  } catch (e) {
    return {
      error: e,
    };
  }
};

// run boots the Wasm instance once and waits for the Go side to signal
// readiness via the workers.ready import. The ctx argument is the runtime
// context object produced by the runtime's createRuntimeContext; the Go
// side reads it as js.Global().Get("context") and registers its trigger
// handlers on ctx.binding.
export async function run(ctx) {
  if (mod === undefined) {
    mod = await loadModule();
  }
  const go = new Go();

  let ready;
  const readyPromise = new Promise((resolve) => {
    ready = resolve;
  });
  const instance = new WebAssembly.Instance(mod, {
    ...go.importObject,
    workers: {
      ready: () => {
        ready();
      },
    },
  });
  go.run(instance, ctx);
  await readyPromise;
}
