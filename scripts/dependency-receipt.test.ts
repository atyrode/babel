import { copyFile, mkdir, mkdtemp, rename, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { expect, test } from "bun:test";
import { checkDependencyReceipt, recordDependencyReceipt } from "./dependency-receipt.ts";

// The artifact's own checksum file remains unchanged in every case. Delivery must compare
// actual bytes and the complete bundle set against the gate, not trust a rebuilt sidecar.
test.each(["changed", "missing", "extra", "wrong-role", "unknown-role", "nested-extra"])(
  "rejects a %s dependency before it can be delivered",
  async (difference) => {
    const root = await mkdtemp(join(tmpdir(), "babel-dependency-receipt-"));
    try {
      const dependencies = join(root, "deps");
      const hardened = join(dependencies, "hardened");
      const inRealm = join(dependencies, "in-realm");
      await mkdir(hardened, { recursive: true });
      await mkdir(inRealm, { recursive: true });
      const omp = join(hardened, "atyrode.omp.manifold-plugin.json");
      const code = join(inRealm, "atyrode.code.manifold-plugin.json");
      await writeFile(omp, '{"id":"atyrode.omp","source":"verified"}\n');
      await writeFile(code, '{"id":"atyrode.code","source":"verified"}\n');
      const receipt = join(root, "verified.SHA256SUMS");
      await recordDependencyReceipt(receipt, hardened, inRealm);
      await copyFile(receipt, join(dependencies, "SHA256SUMS"));
      // A mutation must cause the refusal; an always-refusing checker is not a safe gate.
      await checkDependencyReceipt(receipt, dependencies);

      switch (difference) {
        case "changed":
          await writeFile(omp, '{"id":"atyrode.omp","source":"rebuilt differently"}\n');
          break;
        case "missing":
          await rm(omp);
          break;
        case "extra":
          await copyFile(omp, join(hardened, "extra.manifold-plugin.json"));
          break;
        case "wrong-role":
          await rename(omp, join(inRealm, "atyrode.omp.manifold-plugin.json"));
          break;
        case "unknown-role": {
          const other = join(dependencies, "other");
          await mkdir(other);
          await copyFile(omp, join(other, "extra.manifold-plugin.json"));
          break;
        }
        case "nested-extra": {
          const nested = join(hardened, "nested");
          await mkdir(nested);
          await copyFile(omp, join(nested, "extra.manifold-plugin.json"));
          break;
        }
      }
      await expect(checkDependencyReceipt(receipt, dependencies)).rejects.toThrow();
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  },
);
