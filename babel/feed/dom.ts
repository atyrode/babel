import { GlobalRegistrator } from "@happy-dom/global-registrator";

/*
  Register Feed's test DOM before ReactDOM is imported. Keep the native transport for the
  unrelated server/native-service tests sharing a bun test process.
*/
if (!GlobalRegistrator.isRegistered) {
  const transport = { fetch, Headers, Request, Response, FormData, AbortController, AbortSignal };
  GlobalRegistrator.register();
  Object.assign(globalThis, transport);
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
}
