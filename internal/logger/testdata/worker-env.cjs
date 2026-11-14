// Keep Workers bindings separate from the Go process environment.
globalThis.context = {
  env: { ENV: "production" },
  binding: {},
};
