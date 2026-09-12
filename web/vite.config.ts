import { defineConfig } from "vite";

export default defineConfig({
  base: "./",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  // The dev server is the built page with hot reload in front of a real
  // `babel web` on 8850: the API, the bootstrap exchange and the session
  // cookie all go to that process, so a source edit shows without a restart
  // and without spending a second launch link.
  server: {
    host: "127.0.0.1",
    port: 8851,
    strictPort: true,
    proxy: {
      // Object form on purpose: the string shorthand rewrites the Host
      // header to the target, and babel web's origin guard compares the
      // page's Origin against the Host it was asked for. Leaving Host as
      // the browser sent it keeps the two equal.
      "/api": { target: "http://127.0.0.1:8850", changeOrigin: false },
    },
  },
});
