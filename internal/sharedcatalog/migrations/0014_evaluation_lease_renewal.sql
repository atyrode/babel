-- An evaluation claim renews its own lease (SPEC.md 4.12, 5.8; issue #219).
--
-- migrations/0013 named expires_at among the columns a claim attempt may not
-- change, so fleet-wide a lease was fixed at its grant and the only transition
-- an attempt could make was claimed -> finished. That was one rule too many.
--
-- A lease is not a deadline for the work. It is how long an unanswered worker
-- keeps its claim, and fixing it at the grant conflated the two: a review that
-- worked longer than the grant lost an assignment it had never stopped
-- holding, and its write-back was then refused for a conflict that had not
-- happened. A lease that can only be fixed at the grant refuses every review
-- longer than the grant, which is what the shared deployment observed - four
-- reviews of 386s, 461s, 556s and 630s under a 240s lease, every one of them
-- refused at the end, and not one vote recorded.
--
-- A worker that is still working says so by renewing, which is exactly what
-- distinguishes it from the crashed worker expiry exists to release. So this
-- migration replaces the trigger with one admitting one further transition and
-- nothing else:
--
--     a claimed attempt whose lease has not lapsed may move its own
--     expires_at strictly forward, leaving every other column alone.
--
-- Forward only, because a renewal is the holder keeping authority it already
-- has: a policy whose lease has since been shortened governs the next claim
-- rather than cutting short a window already granted, and a statement moving
-- an expiry backwards would release an assignment its holder is still working
-- on - which is 0009's reason for heartbeat_at only advancing.
--
-- Unlapsed only, because an expired assignment is the next claimer's to take
-- under a new fence, and resurrecting one would put two live opinions on a
-- single assignment - the failure the fence exists to prevent. Liveness is
-- read from now() rather than clock_timestamp() on purpose: the serialized
-- application path decides renewal against the later clock (see
-- ClaimEvaluation), and a backstop holding a stricter clock than the caller it
-- guards would refuse renewals that path correctly admitted. Its job is to
-- make resurrection impossible, not to re-decide the microsecond.
--
-- The reservation is untouched by any of this, which is what makes the
-- relaxation safe for the accounting 0013 protects: an attempt is charged its
-- full reserved_cost until it reports, live, renewed, expired or abandoned, so
-- a longer lease spends nothing further and a renewal cannot spend the day
-- twice.
--
-- Everything else 0013 settled is unchanged and asserted here in full:
-- identity, subject, run, owner, policy version, day, reservation and
-- claimed_at stay immutable; a finished attempt refuses every update, so a
-- second finish reporting a different cost is still a contradiction rather
-- than a correction; and a finish may not move the lease while it is at it.
-- The finish columns need no clause of their own - 0013's three CHECKs make
-- observed_cost and finished_at disagree with a still-claimed state, so a
-- renewal cannot smuggle a finish through the transition this migration adds.
--
-- The trigger is renamed because it no longer permits only a finish, and a
-- rule whose name misdescribes it to the next operator reading
-- \d evaluation_claims is worse than a longer name. 0013's text is history and
-- is not edited.
--
-- SchemaVersion remains 1: no table, column or plaintext boundary moves, and
-- the compatibility gates still refuse writes from binaries older than the
-- applied migration ledger.

DROP TRIGGER evaluation_claims_finish_only_trg ON evaluation_claims;
DROP FUNCTION evaluation_claims_finish_only();

CREATE FUNCTION evaluation_claims_finish_or_renew() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.state = 'finished' THEN
        RAISE EXCEPTION
            'evaluation claim %/% fence % is finished; a second finish reporting a different cost is a contradiction, not a correction (issue #219)',
            OLD.deployment_id, OLD.claim_id, OLD.fence;
    END IF;
    IF NEW.deployment_id  <> OLD.deployment_id
       OR NEW.claim_id    <> OLD.claim_id
       OR NEW.fence       <> OLD.fence
       OR NEW.subject_kind <> OLD.subject_kind
       OR NEW.subject_id  <> OLD.subject_id
       OR NEW.run_id      <> OLD.run_id
       OR NEW.owner_id    <> OLD.owner_id
       OR NEW.policy_version <> OLD.policy_version
       OR NEW.day         <> OLD.day
       OR NEW.reserved_cost <> OLD.reserved_cost
       OR NEW.claimed_at  <> OLD.claimed_at THEN
        RAISE EXCEPTION
            'an evaluation claim attempt is immutable except for its finish and the renewal of its own lease: identity, day and reservation may not change';
    END IF;
    IF NEW.state = 'claimed' THEN
        -- The renewal. The holder, the fence and the reservation are already
        -- known unchanged, so what is left is the direction and the liveness.
        IF NEW.expires_at <= OLD.expires_at THEN
            RAISE EXCEPTION
                'the lease on evaluation claim %/% fence % runs to % and a renewal offered %; a renewal moves an expiry forward, because moving one backwards would release an assignment its holder is still working on (issue #219)',
                OLD.deployment_id, OLD.claim_id, OLD.fence, OLD.expires_at, NEW.expires_at;
        END IF;
        IF OLD.expires_at <= now() THEN
            RAISE EXCEPTION
                'the lease on evaluation claim %/% fence % expired at %; an expired assignment is taken over under a new fence, never extended, because the next claimer may hold it already (issue #219)',
                OLD.deployment_id, OLD.claim_id, OLD.fence, OLD.expires_at;
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.state <> 'finished' THEN
        RAISE EXCEPTION
            'the only transitions an evaluation claim attempt may make are claimed -> finished and a forward renewal of its own lease';
    END IF;
    IF NEW.expires_at <> OLD.expires_at THEN
        RAISE EXCEPTION
            'a finish reports what an attempt spent and closes it; it does not move the lease as well';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER evaluation_claims_finish_or_renew_trg
    BEFORE UPDATE ON evaluation_claims
    FOR EACH ROW EXECUTE FUNCTION evaluation_claims_finish_or_renew();
