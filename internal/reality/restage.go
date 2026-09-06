package reality

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// restageCandidates reconstructs publication ownership from durable relationships,
// not current entity membership or lifecycle state. Split results were created in
// the split transaction; action results belong to the acceptance, including the
// split's entities and membership entries. Initial membership has no wire record.
// Disputes have no durable origin discriminator: both explicit judgments and
// automatic contradictions store the same rows and actor metadata. Recovery
// retains every durable dispute rather than guessing origin from operator prose.
const restageCandidates = `WITH
accepted AS (
 SELECT a.result_id, p.id AS anchor FROM reality_plan_action a
 JOIN reality_plan_acceptance p ON p.plan_id = a.plan_id
 WHERE a.result_id IS NOT NULL
),
resolutions AS (
 SELECT r.id, COALESCE(a.anchor, r.id) AS anchor
 FROM reality_resolution r LEFT JOIN accepted a ON a.result_id = r.id
),
split_entities AS (
 SELECT m.entity_id, r.anchor FROM reality_resolution_member m
 JOIN reality_resolution s ON s.id = m.resolution_id AND s.resolution_kind = 'split'
 JOIN resolutions r ON r.id = s.id WHERE m.member_role = 'result'
),
candidates AS (
 SELECT 'entity' AS kind, e.id, COALESCE(s.anchor, e.id) AS anchor
 FROM reality_entity e LEFT JOIN split_entities s ON s.entity_id = e.id
 UNION ALL SELECT 'fact', f.id, COALESCE(a.anchor, f.import_id, f.id)
 FROM reality_fact f LEFT JOIN accepted a ON a.result_id = f.id
 UNION ALL SELECT 'answer', id, id FROM reality_answer
 UNION ALL SELECT 'context', id, id FROM reality_context
 UNION ALL SELECT 'import', id, id FROM reality_import
 UNION ALL SELECT 'resolution', id, anchor FROM resolutions
 UNION ALL SELECT 'membership', m.resolution_id || '.' || m.entity_id, r.anchor
 FROM reality_entity_membership m JOIN resolutions r ON r.id = m.resolution_id
 UNION ALL SELECT 'dispute', d.id, COALESCE(a.anchor, d.id)
 FROM reality_dispute d LEFT JOIN accepted a ON a.result_id = d.id
 UNION ALL SELECT 'plan', id, id FROM reality_plan
 UNION ALL SELECT 'acceptance', id, id FROM reality_plan_acceptance
)
SELECT kind, id, anchor FROM candidates c
WHERE NOT EXISTS (SELECT 1 FROM sync_record s WHERE s.record_id = c.id)
ORDER BY anchor, CASE WHEN id = anchor THEN 0 ELSE 1 END, kind, id`

type restageCandidate struct {
	kind PublishedKind
	id string
	anchor string
}

// Restage recovers locally durable canonical records into the configured sync
// journal without publishing. Each operation's missing records and declaration
// commit together, so interruption can resume at the next record or bundle.
func (s *Store) Restage(ctx context.Context) (int, error) {
	if s.sync == nil {
		return 0, fmt.Errorf("reality: restage requires a sync hook")
	}
	rows, err := s.db.QueryContext(ctx, restageCandidates)
	if err != nil {
		return 0, fmt.Errorf("reality: find unstaged records: %w", err)
	}
	var pending []restageCandidate
	for rows.Next() {
		var item restageCandidate
		if err := rows.Scan(&item.kind, &item.id, &item.anchor); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	// Close the discovery cursor before beginning any transaction: durable
	// stores deliberately have one connection.
	count := 0
	for start := 0; start < len(pending); {
		end := start + 1
		for end < len(pending) && pending[end].anchor == pending[start].anchor {
			end++
		}
		added := 0
		err := s.transact(ctx, func(tx *sql.Tx) error {
			set := s.newRecordSet()
			for _, item := range pending[start:end] {
				var exists bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sync_record WHERE record_id = ?)`, item.id).Scan(&exists); err != nil {
					return err
				}
				if exists {
					continue
				}
				wire, err := readRestageRecord(ctx, tx, item)
				if err != nil {
					return fmt.Errorf("reality: recover %s %s: %w", item.kind, item.id, err)
				}
				if err := set.add(staged(item.id, wire)); err != nil {
					return err
				}
				added++
			}
			if added == 0 {
				return nil
			}
			if end-start == 1 && pending[start].id == pending[start].anchor {
				switch pending[start].kind {
				case PublishedImport, PublishedResolution, PublishedAcceptance:
					// These operations declare a set even when only the anchor is missing.
				default:
					_, err := s.stage(ctx, tx, set.records[0])
					return err
				}
			}
			_, err := s.stageSet(ctx, tx, pending[start].anchor, set)
			return err
		})
		if err != nil {
			return count, err
		}
		count += added
		start = end
	}
	return count, nil
}

// readRestageRecord uses the same PublishedRecord/staged encoder as live writes,
// but takes opaque payloads directly from their immutable columns. Decoding into
// today's payload structs would silently discard fields from older records.
func readRestageRecord(ctx context.Context, q querier, item restageCandidate) (PublishedRecord, error) {
	p := PublishedRecord{Schema: RecordSchema, Kind: item.kind, ID: item.id}
	var recorded, authored string
	var err error
	switch item.kind {
	case PublishedEntity:
		err = q.QueryRowContext(ctx, `SELECT schema_version, kind, created_at, payload_json FROM reality_entity WHERE id = ?`, item.id).
			Scan(&p.Schema, &p.EntityKind, &recorded, (*[]byte)(&p.Payload))
	case PublishedFact:
		c := &PublishedClaim{}
		p.Claim = c
		var from, until, observed, authority string
		err = q.QueryRowContext(ctx, `SELECT schema_version, recorded_at, subject_id, predicate, valid_from,
			COALESCE(valid_until, ''), observed_at, authority_kind, authority_id, authority_at,
			confidence, sensitivity, COALESCE(supersedes, ''), payload_json FROM reality_fact WHERE id = ?`, item.id).
			Scan(&p.Schema, &recorded, &c.SubjectID, &c.Predicate, &from, &until, &observed,
				&c.AuthorityKind, &c.AuthorityID, &authority, &c.Confidence, &c.Sensitivity, &c.Supersedes, (*[]byte)(&p.Payload))
		if err == nil {
			err = restageTimes([]string{from, until, observed, authority}, []*time.Time{&c.ValidFrom, &c.ValidUntil, &c.ObservedAt, &c.AuthorityAt})
		}
	case PublishedAnswer:
		p.Response = &PublishedResponse{}
		err = q.QueryRowContext(ctx, `SELECT schema_version, recorded_at, question_id, outcome,
			COALESCE(context_id, ''), author, answered_at, payload_json FROM reality_answer WHERE id = ?`, item.id).
			Scan(&p.Schema, &recorded, &p.Response.QuestionID, &p.Response.Outcome, &p.Response.ContextID, &p.Author, &authored, (*[]byte)(&p.Payload))
	case PublishedContext:
		err = q.QueryRowContext(ctx, `SELECT recorded_at, author, supplied_at, payload_json FROM reality_context WHERE id = ?`, item.id).
			Scan(&recorded, &p.Author, &authored, (*[]byte)(&p.Payload))
	case PublishedImport:
		p.Batch = &PublishedBatch{}
		err = q.QueryRowContext(ctx, `SELECT imported_at, source_id, batch_key, fact_count FROM reality_import WHERE id = ?`, item.id).
			Scan(&recorded, &p.Batch.SourceID, &p.Batch.BatchKey, &p.Batch.FactCount)
	case PublishedResolution:
		p.Identity = &PublishedIdentity{}
		err = q.QueryRowContext(ctx, `SELECT recorded_at, resolution_kind, COALESCE(reverses_id, ''), actor, payload_json FROM reality_resolution WHERE id = ?`, item.id).
			Scan(&recorded, &p.Identity.Kind, &p.Identity.ReversesID, &p.Author, (*[]byte)(&p.Payload))
		if err == nil {
			p.Identity.SourceIDs, err = queryStrings(ctx, q, `SELECT entity_id FROM reality_resolution_member WHERE resolution_id = ? AND member_role = 'source' ORDER BY position`, item.id)
		}
		if err == nil {
			p.Identity.ResultIDs, err = queryStrings(ctx, q, `SELECT entity_id FROM reality_resolution_member WHERE resolution_id = ? AND member_role = 'result' ORDER BY position`, item.id)
		}
	case PublishedMembership:
		p.Membership = &PublishedMembershipEntry{}
		err = q.QueryRowContext(ctx, `SELECT recorded_at, entity_id, role, canonical_id, resolution_id
			FROM reality_entity_membership WHERE entity_id = substr(?, instr(?, '.') + 1)
			AND resolution_id || '.' || entity_id = ?`, item.id, item.id, item.id).
			Scan(&recorded, &p.Membership.EntityID, &p.Membership.Role, &p.Membership.CanonicalID, &p.Membership.ResolutionID)
	case PublishedDispute:
		p.Contradiction = &PublishedContradiction{}
		err = q.QueryRowContext(ctx, `SELECT d.schema_version, d.created_at, d.subject_id, d.predicate, d.payload_json,
			(SELECT actor FROM reality_dispute_event e WHERE e.dispute_id = d.id ORDER BY seq LIMIT 1)
			FROM reality_dispute d WHERE d.id = ?`, item.id).
			Scan(&p.Schema, &recorded, &p.Contradiction.SubjectID, &p.Contradiction.Predicate, (*[]byte)(&p.Payload), &p.Author)
		if err == nil {
			p.Contradiction.FactIDs, err = queryStrings(ctx, q, `SELECT fact_id FROM reality_dispute_member WHERE dispute_id = ? ORDER BY fact_id`, item.id)
		}
	case PublishedPlan:
		p.Interpretation = &PublishedInterpretation{Actions: make([]PublishedAction, 0)}
		err = q.QueryRowContext(ctx, `SELECT schema_version, created_at, question_id, answer_id, interpreter_version, payload_json FROM reality_plan WHERE id = ?`, item.id).
			Scan(&p.Schema, &recorded, &p.Interpretation.QuestionID, &p.Interpretation.AnswerID, &p.Interpretation.InterpreterVersion, (*[]byte)(&p.Payload))
		if err == nil {
			p.Interpretation.Actions, err = readRestageActions(ctx, q, item.id)
		}
	case PublishedAcceptance:
		p.Approval = &PublishedApproval{}
		err = q.QueryRowContext(ctx, `SELECT recorded_at, plan_id, COALESCE(context_id, ''), actor, payload_json FROM reality_plan_acceptance WHERE id = ?`, item.id).
			Scan(&recorded, &p.Approval.PlanID, &p.Approval.ContextID, &p.Author, (*[]byte)(&p.Payload))
	default:
		return p, fmt.Errorf("unknown publication kind %q", item.kind)
	}
	if err != nil {
		return p, err
	}
	err = restageTimes([]string{recorded, authored}, []*time.Time{&p.RecordedAt, &p.AuthoredAt})
	return p, err
}

func restageTimes(values []string, targets []*time.Time) error {
	for i, value := range values {
		if value == "" {
			continue
		}
		parsed, err := parseTime(value)
		if err != nil {
			return err
		}
		*targets[i] = parsed
	}
	return nil
}

func readRestageActions(ctx context.Context, q querier, planID string) ([]PublishedAction, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, position, action_kind, payload_json FROM reality_plan_action WHERE plan_id = ? ORDER BY position`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actions := make([]PublishedAction, 0)
	for rows.Next() {
		var action PublishedAction
		if err := rows.Scan(&action.ID, &action.Position, &action.Kind, (*[]byte)(&action.Payload)); err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}
