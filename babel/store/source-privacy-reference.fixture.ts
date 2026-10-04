// Frozen recursive SQL from atyrode/babel@1faeb855f8b05201d47048e66cc7c0bdb13ac214.
// Keep independent of production arm definitions so missing maintenance paths remain observable.
export const frozenSourcePrivacyReference = {
  sql: String.raw`WITH RECURSIVE
projection_state(ready) AS MATERIALIZED (SELECT (SELECT count(*) FROM source_dependency_progress WHERE complete=1)=19),
additional(id) AS MATERIALIZED (SELECT value FROM json_each(?)),
active_taint(kind,id) AS (
  SELECT 'session',selector FROM session_exclusions WHERE (SELECT ready FROM projection_state)=1
  UNION
  SELECT 'session',id FROM additional WHERE (SELECT ready FROM projection_state)=1
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='dependency'
      AND d.input_kind=t.kind AND d.input_id=t.id
    WHERE d.input_kind IN ('session','run','record','root','capture','map','entity','question') AND (d.consumer_record_kind IS NULL OR EXISTS (
    SELECT 1 FROM records r WHERE r.id=d.consumer_id AND r.kind=d.consumer_record_kind))
  AND CASE d.input_catalog
    WHEN 'record' THEN EXISTS (SELECT 1 FROM records r WHERE r.id=d.input_id
      AND (d.input_record_kind IS NULL OR r.kind=d.input_record_kind))
    WHEN 'entity' THEN EXISTS (SELECT 1 FROM entities e WHERE e.id=d.input_id)
    WHEN 'question' THEN EXISTS (SELECT 1 FROM questions q WHERE q.id=d.input_id)
    WHEN 'capture' THEN EXISTS (SELECT 1 FROM transcript_map_captures c WHERE c.id=d.input_id)
    WHEN 'map_node_summary' THEN
      EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=d.input_id)
      OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id=d.input_id)
    ELSE 1 END
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_generic ON d.relation='dependency' AND d.input_id=t.id
    WHERE t.kind IN ('record','capture','map','plan')
      AND (t.kind<>'record' OR d.input_kind IN ('hypothesis','observation','finding','proposal'))
      AND (t.kind<>'plan' OR d.input_kind='plan')
      AND (d.input_kind IS NULL OR d.input_kind NOT IN ('session','run','record','root','capture','map','entity','question'))
      AND t.kind=CASE
  WHEN d.input_kind IN ('session','run','record','root','capture','map','entity','question') THEN d.input_kind
  WHEN EXISTS (SELECT 1 FROM records r WHERE r.id=d.input_id AND r.kind=d.input_kind) THEN 'record'
  WHEN EXISTS (SELECT 1 FROM transcript_map_captures c WHERE c.id=d.input_id) THEN 'capture'
  WHEN EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=d.input_id)
    OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id=d.input_id)
    OR EXISTS (SELECT 1 FROM transcript_map_versions v WHERE v.id=d.input_id)
    OR EXISTS (SELECT 1 FROM transcript_map_plans p WHERE p.id=d.input_id) THEN 'map'
  ELSE d.input_kind END AND (d.consumer_record_kind IS NULL OR EXISTS (
    SELECT 1 FROM records r WHERE r.id=d.consumer_id AND r.kind=d.consumer_record_kind))
  AND CASE d.input_catalog
    WHEN 'record' THEN EXISTS (SELECT 1 FROM records r WHERE r.id=d.input_id
      AND (d.input_record_kind IS NULL OR r.kind=d.input_record_kind))
    WHEN 'entity' THEN EXISTS (SELECT 1 FROM entities e WHERE e.id=d.input_id)
    WHEN 'question' THEN EXISTS (SELECT 1 FROM questions q WHERE q.id=d.input_id)
    WHEN 'capture' THEN EXISTS (SELECT 1 FROM transcript_map_captures c WHERE c.id=d.input_id)
    WHEN 'map_node_summary' THEN
      EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=d.input_id)
      OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id=d.input_id)
    ELSE 1 END
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN sessions s ON t.kind='session' AND s.selector=t.id
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='dependency'
      AND d.input_kind='session' AND s.source_id=d.input_id
    WHERE NOT EXISTS (SELECT 1 FROM sessions exact WHERE exact.selector=d.input_id)
  AND substr(d.input_id,1,instr(d.input_id,'/')-1)
    NOT IN ('omp','codex','claude') AND (d.consumer_record_kind IS NULL OR EXISTS (
    SELECT 1 FROM records r WHERE r.id=d.consumer_id AND r.kind=d.consumer_record_kind))
  AND CASE d.input_catalog
    WHEN 'record' THEN EXISTS (SELECT 1 FROM records r WHERE r.id=d.input_id
      AND (d.input_record_kind IS NULL OR r.kind=d.input_record_kind))
    WHEN 'entity' THEN EXISTS (SELECT 1 FROM entities e WHERE e.id=d.input_id)
    WHEN 'question' THEN EXISTS (SELECT 1 FROM questions q WHERE q.id=d.input_id)
    WHEN 'capture' THEN EXISTS (SELECT 1 FROM transcript_map_captures c WHERE c.id=d.input_id)
    WHEN 'map_node_summary' THEN
      EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=d.input_id)
      OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id=d.input_id)
    ELSE 1 END
  UNION
  -- A prepare producer's payload was also a document of each consuming run. The join stays
  -- live so later job assignment, preparation binding and producer updates cannot go stale.
  SELECT 'run',r.id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_linked ON d.relation='dependency'
      AND d.input_kind=t.kind AND d.input_id=t.id
    CROSS JOIN runs p ON p.id=d.producer_run_id
    CROSS JOIN runs r ON r.prepare_job_id=p.job_id
    WHERE d.producer_run_id IS NOT NULL AND d.input_kind IN ('session','run','record','root','capture','map','entity','question')
  UNION
  SELECT 'run',r.id FROM active_taint t
    CROSS JOIN sessions s ON t.kind='session' AND s.selector=t.id
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_linked ON d.relation='dependency'
      AND d.input_kind='session' AND s.source_id=d.input_id
    CROSS JOIN runs p ON p.id=d.producer_run_id
    CROSS JOIN runs r ON r.prepare_job_id=p.job_id
    WHERE d.producer_run_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM sessions exact WHERE exact.selector=d.input_id)
  AND substr(d.input_id,1,instr(d.input_id,'/')-1)
    NOT IN ('omp','codex','claude')
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='title' AND d.input_id=title.selector
    WHERE d.detail=title.title
  UNION
  SELECT 'run',r.id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='title' AND d.input_id=title.selector
    CROSS JOIN runs p ON p.id=d.producer_run_id
    CROSS JOIN runs r ON r.prepare_job_id=p.job_id
    WHERE d.producer_run_id IS NOT NULL AND d.detail=title.title
  UNION
  SELECT d.consumer_kind,d.consumer_id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN sessions s ON s.selector=title.selector
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='current_title' AND d.input_id=title.selector
    WHERE s.title_provenance='inferred' AND s.title=title.title AND (d.consumer_record_kind IS NULL OR EXISTS (
    SELECT 1 FROM records r WHERE r.id=d.consumer_id AND r.kind=d.consumer_record_kind))
  AND CASE d.input_catalog
    WHEN 'record' THEN EXISTS (SELECT 1 FROM records r WHERE r.id=d.input_id
      AND (d.input_record_kind IS NULL OR r.kind=d.input_record_kind))
    WHEN 'entity' THEN EXISTS (SELECT 1 FROM entities e WHERE e.id=d.input_id)
    WHEN 'question' THEN EXISTS (SELECT 1 FROM questions q WHERE q.id=d.input_id)
    WHEN 'capture' THEN EXISTS (SELECT 1 FROM transcript_map_captures c WHERE c.id=d.input_id)
    WHEN 'map_node_summary' THEN
      EXISTS (SELECT 1 FROM transcript_map_nodes n WHERE n.id=d.input_id)
      OR EXISTS (SELECT 1 FROM transcript_map_summaries s WHERE s.id=d.input_id)
    ELSE 1 END
  UNION
  SELECT 'run',consumer.id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN edges e ON e.to_kind='session' AND e.to_id=title.selector
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='legacy_review' AND d.input_id=e.from_id
    CROSS JOIN runs consumer ON consumer.id=d.consumer_id
    WHERE consumer.started_at>=title.inferred_at
  UNION
  SELECT 'run',consumer.id FROM active_taint t
    CROSS JOIN session_titles title ON t.kind='run' AND title.run_id=t.id
    CROSS JOIN edges e ON e.to_kind='session' AND e.to_id=title.selector
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='legacy_review' AND d.input_id=e.from_id
    CROSS JOIN runs p ON p.id=d.producer_run_id
    CROSS JOIN runs consumer ON consumer.prepare_job_id=p.job_id
    WHERE d.producer_run_id IS NOT NULL AND consumer.started_at>=title.inferred_at
  UNION
  SELECT 'capture',p.capture_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_id ON d.relation='binding_capture' AND t.kind='map' AND d.input_id=t.id
    CROSS JOIN transcript_map_versions v ON v.id=d.consumer_id
    CROSS JOIN transcript_map_plans p ON p.id=v.plan_id
  UNION
  -- Applied output identities remain joined to the current catalog. Accepting model wording
  -- attributes the act to the operator without cutting its proposal/proposing-run lineage.
  SELECT 'entity',e.id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN entities e ON e.id=d.input_id
  UNION
  SELECT 'entity',f.entity_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN facts f ON f.id=d.input_id
  UNION
  SELECT 'entity',m.entity_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN resolution_members m ON m.resolution_id=d.input_id
  UNION
  SELECT 'record',f.record_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN filings f ON f.id=d.input_id
  UNION
  SELECT 'entity',f.entity_id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN filings f ON f.id=d.input_id
  UNION
  SELECT 'record',r.id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN records r ON r.id=d.input_id
  UNION
  SELECT 'question',q.id FROM active_taint t
    CROSS JOIN source_dependency_edges d INDEXED BY source_dependency_edges_consumer ON d.relation='applied_result' AND t.kind='plan' AND d.consumer_id=t.id
    CROSS JOIN questions q ON q.id=d.input_id
),
conservative_state AS MATERIALIZED (
  SELECT 1 FROM projection_state WHERE ready=0
    AND (EXISTS (SELECT 1 FROM session_exclusions) OR EXISTS (SELECT 1 FROM additional))
),
conservative(kind,id) AS (
  SELECT 'session',selector FROM conservative_state CROSS JOIN session_exclusions
  UNION SELECT 'session',id FROM conservative_state CROSS JOIN additional
  UNION SELECT 'session',selector FROM conservative_state CROSS JOIN sessions
  UNION SELECT 'record',id FROM conservative_state CROSS JOIN records
  UNION SELECT 'root',root_id FROM conservative_state CROSS JOIN records
  UNION SELECT 'run',id FROM conservative_state CROSS JOIN runs
  UNION SELECT 'capture',id FROM conservative_state CROSS JOIN transcript_map_captures
  UNION SELECT 'entity',id FROM conservative_state CROSS JOIN entities
  UNION SELECT 'question',id FROM conservative_state CROSS JOIN questions
  UNION SELECT 'plan',id FROM conservative_state CROSS JOIN plans
  UNION SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_plans
  UNION SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_nodes
  UNION SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_versions
  UNION SELECT 'map',id FROM conservative_state CROSS JOIN transcript_map_summaries
),
tainted(kind,id) AS (
  SELECT kind,id FROM active_taint UNION ALL SELECT kind,id FROM conservative
)`,
  params: ["[]"] as const,
};
