import "./wasm_exec.js";
import { createRuntimeContext, loadModule } from "./runtime.mjs";

let mod;
let readyPromise;
const binding = {};
const runtimeContext = createRuntimeContext({
  env: undefined,
  ctx: undefined,
  binding,
});

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

async function ensureRunning() {
  if (readyPromise !== undefined) {
    return readyPromise;
  }
  const initPromise = (async () => {
    if (mod === undefined) {
      mod = await loadModule();
    }
    const go = new Go();
    let ready;
    const readyGate = new Promise((resolve) => {
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
    go.run(instance, runtimeContext).finally(() => {
      if (readyPromise === initPromise) {
        readyPromise = undefined;
      }
    });
    await readyGate;
  })();
  readyPromise = initPromise;
  try {
    await initPromise;
  } catch (err) {
    if (readyPromise === initPromise) {
      readyPromise = undefined;
    }
    throw err;
  }
}

async function fetch(req, env, ctx) {
  runtimeContext.env = env;
  runtimeContext.ctx = ctx;
  await ensureRunning();
  return binding.handleRequest(req);
}

async function scheduled(event, env, ctx) {
  runtimeContext.env = env;
  runtimeContext.ctx = ctx;
  await ensureRunning();
  return binding.runScheduler(event);
}

async function queue(batch, env, ctx) {
  runtimeContext.env = env;
  runtimeContext.ctx = ctx;
  await ensureRunning();
  return binding.handleQueueMessageBatch(batch);
}

async function onRequest(ctx) {
  const { request, env } = ctx;
  runtimeContext.env = env;
  runtimeContext.ctx = ctx;
  await ensureRunning();
  return binding.handleRequest(request);
}

export default {
  fetch,
  scheduled,
  queue,
  onRequest,
};
