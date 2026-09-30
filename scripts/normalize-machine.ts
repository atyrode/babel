import { realpathSync } from "node:fs";
import { isAbsolute, relative, resolve, sep } from "node:path";
import ts from "typescript";

/** Bun labels bundled modules with their source path. SDK paths vary with sibling symlinks,
 * even though their executable source is identical. Rewrite only top-level Bun module
 * comments that resolve inside the pinned SDK; a string containing the same text is code. */
export function normalizeSdkModuleComments(
  source: string,
  sdkDirectory: string,
  buildDirectory: string,
): string {
  const sdk = realpathSync(sdkDirectory);
  // Parsing, not token-scanning, is necessary: a scanner without parser context cannot rescan
  // template substitutions and may mistake the rest of the bundle for one long template.
  const tree = ts.createSourceFile(
    "machine.js",
    source,
    ts.ScriptTarget.Latest,
    false,
    ts.ScriptKind.JS,
  );
  const output: string[] = [];
  let kept = 0;
  for (const statement of tree.statements) {
    for (const range of ts.getLeadingCommentRanges(source, statement.getFullStart()) ?? []) {
      if (range.kind !== ts.SyntaxKind.SingleLineCommentTrivia || range.pos < kept) continue;
      const start = range.pos;
      if (start > 0 && source[start - 1] !== "\n") continue;
      const comment = source.slice(start, range.end);
      if (!comment.startsWith("// ")) continue;
      const label = comment.slice(3);
      if (!label.startsWith("../") && !label.startsWith("./") && !isAbsolute(label)) continue;

      let path: string;
      try {
        path = realpathSync(resolve(buildDirectory, label));
      } catch {
        // Other comments are not Bun labels for an existing SDK module.
        continue;
      }
      const inside = relative(sdk, path);
      if (!inside.startsWith(`packages${sep}`) && !inside.startsWith(`node_modules${sep}`))
        continue;
      const canonical = `// ../manifold/${inside.split(sep).join("/")}`;
      if (comment === canonical) continue;
      output.push(source.slice(kept, start), canonical);
      kept = range.end;
    }
  }
  if (output.length === 0) return source;
  output.push(source.slice(kept));
  return output.join("");
}
