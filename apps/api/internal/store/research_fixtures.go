package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/veighnsche/find-income-dashboard/api/internal/researchcontract"
)

// Deterministic research fixtures. Lanes B/D reuse these seeds in their own
// tests; production code must never call them.
const (
	FixtureActorKind = "administrator"
	FixtureActorID   = "fixture-owner"
	FixtureTime      = "2026-09-24T00:00:00.000000000Z"
)

// FixtureActor returns the fixed research test actor.
func FixtureActor() Actor { return Actor{Kind: FixtureActorKind, ID: FixtureActorID} }

// FixtureSHA256 returns the lowercase hex sha256 of seed (64 chars), for
// fingerprint/hash/content columns in tests.
func FixtureSHA256(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// FixtureRequestDescriptor returns a valid small exact-request descriptor.
func FixtureRequestDescriptor() researchcontract.RequestDescriptor {
	return researchcontract.RequestDescriptor{
		Operation:  researchcontract.OperationFetch,
		Backend:    "fixture-backend",
		Method:     "GET",
		URLOrQuery: "https://example.com/jobs/42",
		Params:     []researchcontract.Param{{Name: "page", Value: "1"}},
	}
}

// FixtureExecutorIdentity returns a fixed executor identity for tests.
func FixtureExecutorIdentity() researchcontract.ExecutorIdentity {
	return researchcontract.ExecutorIdentity{
		Backend: "fixture-backend",
		Version: "fixture-1",
		Digest:  "sha256:fixture",
	}
}

// SeedResearchRound inserts a running round plus one reserved attempt. Fresh
// databases seed preferences v1 at Open, which the round references. Only one
// active round fits per database (one_active_round index); seed extras with
// SeedResearchRoundState and a terminal state.
func SeedResearchRound(ctx context.Context, db ResearchDB, roundID, attemptID string) error {
	return SeedResearchRoundState(ctx, db, roundID, attemptID, "running")
}

// SeedResearchRoundState inserts a round in the given state plus one reserved
// attempt.
func SeedResearchRoundState(ctx context.Context, db ResearchDB, roundID, attemptID, state string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO rounds
  (id,actor_kind,actor_id,request_key,request_sha256,intent,outcome,
   initial_profile_version,profile_version,scope_json,state,revision,generation,
   deadline_at,request_limit,item_limit,tool_limit,turn_limit,created_at,updated_at)
  VALUES (?,?,?,?,?,'fixture intent','fixture-outcome',1,1,'{}',?,1,1,
   '2026-09-25T00:00:00Z',60,60,60,8,?,?)`,
		roundID, FixtureActorKind, FixtureActorID, "req-"+roundID,
		FixtureSHA256("req-"+roundID), state, FixtureTime, FixtureTime)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO round_attempts
  (id,round_id,request_key,request_sha256,operation,resource_id,generation,state,
   requests_reserved,items_reserved,tools_reserved,turns_reserved,created_at,updated_at)
  VALUES (?,?,?,?,?,'research',1,'reserved',0,0,0,0,?,?)`,
		attemptID, roundID, "attempt-"+attemptID,
		FixtureSHA256("attempt-"+attemptID), "research.fetch", FixtureTime, FixtureTime)
	return err
}

// SeedResearchCompany inserts a minimal company row.
func SeedResearchCompany(ctx context.Context, db ResearchDB, id, name string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO companies
  (id,name,created_at,updated_at) VALUES (?,?,?,?)`,
		id, name, FixtureTime, FixtureTime)
	return err
}

// SeedResearchOpportunity inserts a minimal opportunity row. The insert fires
// the standard qualification input-version triggers.
func SeedResearchOpportunity(ctx context.Context, db ResearchDB, id, companyID, title string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO opportunities
  (id,company_id,title,kind,stage,created_at,updated_at) VALUES (?,?,?,'employment','new',?,?)`,
		id, companyID, title, FixtureTime, FixtureTime)
	return err
}

// SeedResearchJevAttempt inserts a minimal dispatched jev_attempts row for
// dynamic-assessment binding tests.
func SeedResearchJevAttempt(ctx context.Context, db ResearchDB, jevID, roundID, attemptID string, step int64) error {
	_, err := db.ExecContext(ctx, `INSERT INTO jev_attempts
  (id,round_id,round_attempt_id,step_index,purpose,input_sha256,source_refs_json,
   candidate_set_json,profile_version,rubric_version,status,created_at)
  VALUES (?,?,?,?, 'fixture',?, '[]','[]',1,'rubric-v1','dispatched',?)`,
		jevID, roundID, attemptID, step, FixtureSHA256(jevID), FixtureTime)
	return err
}
