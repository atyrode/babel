import type { PluginDatabase, SqlRow } from "@manifold/plugin";
import {
  RECORD_KINDS,
  RECIPE_READING_BATCH,
  RECIPE_READING_TEXT_MAX,
  type RecipeRecords,
} from "../contract.ts";
import { nameableRecordSql } from "./schema.ts";

/*
 * One SQL snapshot counts the entire retained corpus and hands off only a bounded page of
 * current eligible revisions. The imported scalar is NOT attribution evidence when the worker
 * retained its Recipes array: that scalar is only the first member of a possibly multi-recipe run.
 * Native runs retain their full selection in preparation.recipes. An explicit empty or malformed
 * selection never falls through to the scalar. Record-local recipe_id never substitutes for a run.
 */
export async function recipeRecords(db: PluginDatabase): Promise<RecipeRecords> {
  const claimPath = `CASE r.kind WHEN 'hypothesis' THEN '$.statement'
                       WHEN 'observation' THEN '$.claim' WHEN 'finding' THEN '$.pattern'
                       WHEN 'proposal' THEN '$.outcome' END`;
  const rows = await db.query<SqlRow>(
    `WITH run_documents AS (
       SELECT id, recipe_id,
              CASE WHEN json_valid(payload) THEN payload ELSE '{}' END AS document,
              CASE WHEN json_valid(preparation) THEN preparation ELSE '{}' END AS preparation
         FROM runs
     ), selections AS (
       SELECT id,
              CASE WHEN json_type(document, '$.worker.Recipes') IS NOT NULL
                     THEN CASE WHEN json_type(document, '$.worker.Recipes') = 'array'
                               THEN json_extract(document, '$.worker.Recipes') ELSE '[]' END
                   WHEN json_type(preparation, '$.recipes') IS NOT NULL
                     THEN CASE WHEN json_type(preparation, '$.recipes') = 'array'
                               THEN json_extract(preparation, '$.recipes') ELSE '[]' END
                   WHEN COALESCE(recipe_id, '') <> '' THEN json_array(json_object('id', recipe_id))
                   ELSE '[]' END AS recipes
         FROM run_documents
     ), attributed AS (
       SELECT r.id, r.seq, r.kind, r.created_at,
              COALESCE(json_array_length(s.recipes), 0) AS recipe_count,
              CASE WHEN json_type(s.recipes, '$[0].id') = 'text'
                   THEN json_extract(s.recipes, '$[0].id') ELSE '' END AS recipe,
              (${nameableRecordSql("r.id")}
               AND r.kind IN (${RECORD_KINDS.map(() => "?").join(",")})
               AND NOT EXISTS (SELECT 1 FROM records h WHERE h.supersedes_id = r.id)) AS current
         FROM records r LEFT JOIN selections s ON s.id = r.run_id
     ), classified AS MATERIALIZED (
       SELECT *, CASE WHEN NOT current THEN 'excluded'
                      WHEN recipe_count > 1 THEN 'multi'
                      WHEN recipe_count = 1 AND trim(recipe) <> '' THEN 'eligible'
                      ELSE 'excluded' END AS category
         FROM attributed
     ), sample AS (
       SELECT * FROM classified WHERE category = 'eligible'
        ORDER BY julianday(created_at) DESC, id DESC LIMIT ?
     ), claims AS (
       SELECT s.*,
              COALESCE(NULLIF(
                CASE WHEN json_valid(r.payload)
                     THEN CASE WHEN json_type(r.payload, ${claimPath}) = 'text'
                               THEN json_extract(r.payload, ${claimPath}) END END, ''), r.title) AS claim
         FROM sample s JOIN records r ON r.id = s.id
     )
     SELECT 'count' AS row_type, category, CASE WHEN category = 'eligible' THEN recipe ELSE '' END AS recipe,
            COUNT(*) AS n, NULL AS id, NULL AS seq, NULL AS kind, NULL AS text
       FROM classified GROUP BY category, CASE WHEN category = 'eligible' THEN recipe ELSE '' END
     UNION ALL
     SELECT 'record', category, recipe, 0, id, seq, kind,
            CASE WHEN length(claim) <= ? THEN claim END FROM claims`,
    [...RECORD_KINDS, RECIPE_READING_BATCH, RECIPE_READING_TEXT_MAX],
  );
  const result: RecipeRecords = {
    total: 0,
    eligible: 0,
    multiRecipe: 0,
    excluded: 0,
    recipes: [],
    records: [],
  };
  for (const row of rows) {
    if (row["row_type"] === "count") {
      const n = Number(row["n"]);
      result.total += n;
      if (row["category"] === "eligible") {
        result.eligible += n;
        result.recipes.push({ recipeId: String(row["recipe"]), eligible: n });
      } else if (row["category"] === "multi") result.multiRecipe += n;
      else result.excluded += n;
      continue;
    }
    const text = typeof row["text"] === "string" ? row["text"] : null;
    result.records.push({
      recordId: String(row["id"]),
      revision: Number(row["seq"]),
      kind: row["kind"] as RecipeRecords["records"][number]["kind"],
      recipeId: String(row["recipe"]),
      text: text !== null && text.length <= RECIPE_READING_TEXT_MAX ? text : null,
    });
  }
  result.recipes.sort((a, b) => a.recipeId.localeCompare(b.recipeId));
  return result;
}
