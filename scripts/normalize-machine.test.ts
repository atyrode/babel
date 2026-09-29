import { mkdir, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { expect, test } from "bun:test";
import { normalizeSdkModuleComments } from "./normalize-machine.ts";

test("the same SDK module gets the same bundle bytes through a real or symlinked sibling", async () => {
  const root = await mkdtemp(join(tmpdir(), "babel-machine-label-"));
  try {
    const buildDirectory = join(root, "babel");
    const sdk = join(root, "sdk");
    const module = "packages/protocol/src/capabilities.ts";
    const dependency = "node_modules/.bun/zod@4.4.3/node_modules/zod/v4/core/core.js";
    await mkdir(buildDirectory);
    await mkdir(join(sdk, "packages/protocol/src"), { recursive: true });
    await writeFile(join(sdk, module), "export const permitted = true;\n");
    await mkdir(join(sdk, dirname(dependency)), { recursive: true });
    await writeFile(join(sdk, dependency), "export const zod = true;\n");
    await symlink(sdk, join(root, "sdk-link"));
    const suffix = [
      'const message = "// ../sdk-link/packages/protocol/src/capabilities.ts";',
      "const embedded = `first line ${1}",
      "// ../sdk-link/packages/protocol/src/capabilities.ts`;",
      "// ../unknown/packages/protocol/src/capabilities.ts",
      "export { message, embedded };",
      "",
    ].join("\n");
    const physical = `// ../sdk/${module}\n${suffix}// ../sdk/${dependency}\nexport const value = 1;\n`;
    const linked = `// ../sdk-link/${module}\n${suffix}// ../sdk-link/${dependency}\nexport const value = 1;\n`;
    const canonical = `// ../manifold/${module}\n${suffix}// ../manifold/${dependency}\nexport const value = 1;\n`;
    expect(normalizeSdkModuleComments(physical, sdk, buildDirectory)).toBe(canonical);
    expect(normalizeSdkModuleComments(linked, sdk, buildDirectory)).toBe(canonical);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
