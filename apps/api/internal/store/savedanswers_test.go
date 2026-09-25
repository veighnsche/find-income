package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func savedAnswerTextHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func TestSavedAnswerCreateApprovesVersionOne(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	input := SavedAnswerCreateInput{RequestKey: "answer-remote-1", Text: "I work remotely from Example City.",
		ScopeTags: []string{"work_pattern", "remote"}, ContextNote: "Applies where remote work is explicit.",
		SourceRefs: []SavedAnswerSourceRef{{Kind: SavedAnswerSourceOwnerStatement, Ref: "owner:statement:1"}}}
	value, created, err := s.CreateSavedAnswer(ctx, owner, input)
	if err != nil || !created {
		t.Fatalf("create: %+v %v", value, err)
	}
	if value.ID == "" || value.CurrentVersion != 1 || len(value.Versions) != 1 {
		t.Fatalf("create identity: %+v", value)
	}
	if strings.Join(value.ScopeTags, ",") != "work_pattern,remote" || value.ContextNote != input.ContextNote {
		t.Fatalf("create scope/context: %+v", value)
	}
	version := value.Versions[0]
	if version.Version != 1 || version.Text != input.Text || version.TextSHA256 != savedAnswerTextHash(input.Text) {
		t.Fatalf("version provenance: %+v", version)
	}
	if version.ApprovedBy.ActorKind != owner.Kind || version.ApprovedBy.ActorID != owner.ID ||
		version.ApprovalRequestKey != input.RequestKey {
		t.Fatalf("version approval: %+v", version)
	}
	if _, err := time.Parse(time.RFC3339Nano, version.ApprovedAt); err != nil {
		t.Fatalf("approvedAt not RFC3339: %q", version.ApprovedAt)
	}
	if version.Supersedes != nil || version.ChangeNote != "" {
		t.Fatalf("v1 must not supersede: %+v", version)
	}
	if len(version.SourceRefs) != 1 || version.SourceRefs[0] != input.SourceRefs[0] {
		t.Fatalf("version sources: %+v", version.SourceRefs)
	}
	read, err := s.SavedAnswer(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	rawValue, _ := json.Marshal(value)
	rawRead, _ := json.Marshal(read)
	if string(rawValue) != string(rawRead) {
		t.Fatalf("read differs from create:\n%s\n%s", rawValue, rawRead)
	}
	var audit int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_changes
	  WHERE operation='answer.approve' AND entity_kind='saved_answer' AND entity_id=? AND revision_after=1`,
		value.ID).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("approval audit: %d %v", audit, err)
	}
}

func TestSavedAnswerCreateValidation(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	agent := Actor{Kind: "agent", ID: "codex-1"}
	valid := SavedAnswerCreateInput{RequestKey: "k", Text: "Exact owner wording.",
		ScopeTags: []string{"start_date"}}
	cases := map[string]func(*SavedAnswerCreateInput) Actor{
		"agent actor":        func(input *SavedAnswerCreateInput) Actor { return agent },
		"empty request key":  func(input *SavedAnswerCreateInput) Actor { input.RequestKey = ""; return owner },
		"padded request key": func(input *SavedAnswerCreateInput) Actor { input.RequestKey = " k"; return owner },
		"empty text":         func(input *SavedAnswerCreateInput) Actor { input.Text = ""; return owner },
		"blank text":         func(input *SavedAnswerCreateInput) Actor { input.Text = "   "; return owner },
		"overlong text":      func(input *SavedAnswerCreateInput) Actor { input.Text = strings.Repeat("x", 20001); return owner },
		"invalid utf8 text":  func(input *SavedAnswerCreateInput) Actor { input.Text = "bad\xff"; return owner },
		"empty tag":          func(input *SavedAnswerCreateInput) Actor { input.ScopeTags = []string{""}; return owner },
		"padded tag":         func(input *SavedAnswerCreateInput) Actor { input.ScopeTags = []string{" remote"}; return owner },
		"overlong tag": func(input *SavedAnswerCreateInput) Actor {
			input.ScopeTags = []string{strings.Repeat("t", 65)}
			return owner
		},
		"overlong note": func(input *SavedAnswerCreateInput) Actor { input.ContextNote = strings.Repeat("n", 2001); return owner },
		"bad source kind": func(input *SavedAnswerCreateInput) Actor {
			input.SourceRefs = []SavedAnswerSourceRef{{Kind: "web", Ref: "https://example.test"}}
			return owner
		},
		"empty source ref": func(input *SavedAnswerCreateInput) Actor {
			input.SourceRefs = []SavedAnswerSourceRef{{Kind: SavedAnswerSourceCapture, Ref: ""}}
			return owner
		},
		"overlong excerpt": func(input *SavedAnswerCreateInput) Actor {
			input.SourceRefs = []SavedAnswerSourceRef{{Kind: SavedAnswerSourceCV, Ref: "cv:1", Excerpt: strings.Repeat("e", 2001)}}
			return owner
		},
	}
	for name, mutate := range cases {
		input := valid
		input.ScopeTags = append([]string(nil), valid.ScopeTags...)
		actor := mutate(&input)
		if _, _, err := s.CreateSavedAnswer(ctx, actor, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	many := valid
	many.RequestKey = "many-tags"
	for i := 0; i < 33; i++ {
		many.ScopeTags = append(many.ScopeTags, "tag")
	}
	many.ScopeTags[0] = "tag-0"
	if _, _, err := s.CreateSavedAnswer(ctx, owner, many); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too many tags: %v", err)
	}
}

func TestSavedAnswerCreateIdempotency(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	input := SavedAnswerCreateInput{RequestKey: "idem-create", Text: "Exact wording.",
		ScopeTags: []string{"right_to_work", "right_to_work"}}
	first, created, err := s.CreateSavedAnswer(ctx, owner, input)
	if err != nil || !created {
		t.Fatalf("create: %+v %v", first, err)
	}
	if len(first.ScopeTags) != 1 || first.ScopeTags[0] != "right_to_work" {
		t.Fatalf("tags not deduped: %+v", first.ScopeTags)
	}
	replay, created, err := s.CreateSavedAnswer(ctx, owner, input)
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay: %+v %v %v", replay, created, err)
	}
	changed := input
	changed.Text = "Different wording."
	if _, _, err := s.CreateSavedAnswer(ctx, owner, changed); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed text replay: %v", err)
	}
	retagged := input
	retagged.ScopeTags = []string{"other"}
	if _, _, err := s.CreateSavedAnswer(ctx, owner, retagged); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed tags replay: %v", err)
	}
	fresh := input
	fresh.RequestKey = "idem-create-2"
	second, created, err := s.CreateSavedAnswer(ctx, owner, fresh)
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("fresh key: %+v %v", second, err)
	}
	emptyTags := SavedAnswerCreateInput{RequestKey: "no-tags", Text: "Untagged wording."}
	untagged, _, err := s.CreateSavedAnswer(ctx, owner, emptyTags)
	if err != nil {
		t.Fatal(err)
	}
	if untagged.ScopeTags == nil || len(untagged.ScopeTags) != 0 {
		t.Fatalf("empty tags must read back as []: %#v", untagged.ScopeTags)
	}
}

func TestSavedAnswerApproveVersionGuards(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	created, _, err := s.CreateSavedAnswer(ctx, owner, SavedAnswerCreateInput{RequestKey: "v-create",
		Text: "First wording.", ScopeTags: []string{"remote"}, ContextNote: "note",
		SourceRefs: []SavedAnswerSourceRef{{Kind: SavedAnswerSourceCapture, Ref: "capture:1", Excerpt: "city"}}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(created.Versions[0])
	approve := SavedAnswerVersionCreateInput{RequestKey: "v-approve-2", ExpectedVersion: 1,
		Text: "Second wording.", ChangeNote: "Tighten."}
	value, createdNew, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, approve)
	if err != nil || !createdNew || value.CurrentVersion != 2 || len(value.Versions) != 2 {
		t.Fatalf("approve: %+v %v", value, err)
	}
	after, _ := json.Marshal(value.Versions[0])
	if string(before) != string(after) {
		t.Fatalf("v1 changed by v2 approval:\n%s\n%s", before, after)
	}
	second := value.Versions[1]
	if second.Version != 2 || second.Text != approve.Text || second.TextSHA256 != savedAnswerTextHash(approve.Text) ||
		second.ChangeNote != approve.ChangeNote || second.ApprovalRequestKey != approve.RequestKey ||
		second.Supersedes == nil || *second.Supersedes != 1 || len(second.SourceRefs) != 0 {
		t.Fatalf("v2 fields: %+v", second)
	}
	if strings.Join(value.ScopeTags, ",") != "remote" || value.ContextNote != "note" {
		t.Fatalf("answer header changed by version: %+v", value)
	}
	stale := approve
	stale.RequestKey = "v-approve-stale"
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected version: %v", err)
	}
	zero := SavedAnswerVersionCreateInput{RequestKey: "v-zero", ExpectedVersion: 0, Text: "x"}
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, zero); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero expected version: %v", err)
	}
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, "missing", approve); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing answer: %v", err)
	}
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, Actor{Kind: "agent", ID: "codex"}, created.ID,
		SavedAnswerVersionCreateInput{RequestKey: "v-agent", ExpectedVersion: 2, Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("agent approval: %v", err)
	}
	blank := SavedAnswerVersionCreateInput{RequestKey: "v-blank", ExpectedVersion: 2, Text: " "}
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, blank); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank version text: %v", err)
	}
	crossOp := SavedAnswerVersionCreateInput{RequestKey: "v-create", ExpectedVersion: 2, Text: "Second wording."}
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, crossOp); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("create key reused for version: %v", err)
	}
}

func TestSavedAnswerApproveIdempotency(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	created, _, err := s.CreateSavedAnswer(ctx, owner, SavedAnswerCreateInput{RequestKey: "idv-create",
		Text: "Original.", ScopeTags: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	input := SavedAnswerVersionCreateInput{RequestKey: "idv-approve", ExpectedVersion: 1, Text: "Revised."}
	first, createdNew, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, input)
	if err != nil || !createdNew {
		t.Fatalf("approve: %+v %v", first, err)
	}
	replay, createdNew, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, input)
	if err != nil || createdNew || replay.CurrentVersion != 2 || len(replay.Versions) != 2 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	changed := input
	changed.Text = "Other revision."
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, changed); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed text replay: %v", err)
	}
	moved := input
	moved.ExpectedVersion = 2
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID, moved); !errors.Is(err, ErrRoundIdempotencyConflict) {
		t.Fatalf("changed expectation replay: %v", err)
	}
}

func TestSavedAnswerVersionsImmutableAcrossApprovals(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	created, _, err := s.CreateSavedAnswer(ctx, owner, SavedAnswerCreateInput{RequestKey: "imm-create",
		Text: "One.", ScopeTags: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() []string {
		read, err := s.SavedAnswer(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(read.Versions))
		for i, version := range read.Versions {
			raw, _ := json.Marshal(version)
			out[i] = string(raw)
		}
		return out
	}
	v1 := snapshot()
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID,
		SavedAnswerVersionCreateInput{RequestKey: "imm-2", ExpectedVersion: 1, Text: "Two."}); err != nil {
		t.Fatal(err)
	}
	v1v2 := snapshot()
	if len(v1v2) != 2 || v1v2[0] != v1[0] {
		t.Fatalf("v1 mutated: %q vs %q", v1[0], v1v2[0])
	}
	if _, _, err := s.ApproveSavedAnswerVersion(ctx, owner, created.ID,
		SavedAnswerVersionCreateInput{RequestKey: "imm-3", ExpectedVersion: 2, Text: "Three."}); err != nil {
		t.Fatal(err)
	}
	v1v2v3 := snapshot()
	if len(v1v2v3) != 3 || v1v2v3[0] != v1[0] || v1v2v3[1] != v1v2[1] {
		t.Fatal("earlier versions mutated by later approvals")
	}
}

func TestSavedAnswerListScopeFilterAndPagination(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	owner := ownerActor()
	fixtures := []SavedAnswerCreateInput{
		{RequestKey: "list-1", Text: "Remote answer.", ScopeTags: []string{"remote"}},
		{RequestKey: "list-2", Text: "Remote salary answer.", ScopeTags: []string{"remote", "salary"}},
		{RequestKey: "list-3", Text: "Salary answer.", ScopeTags: []string{"salary"}},
	}
	ids := map[string]bool{}
	for _, input := range fixtures {
		value, _, err := s.CreateSavedAnswer(ctx, owner, input)
		if err != nil {
			t.Fatal(err)
		}
		ids[value.ID] = true
	}
	remote, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{Scope: "remote"})
	if err != nil || len(remote.Items) != 2 || remote.NextCursor != "" {
		t.Fatalf("scope filter: %+v %v", remote, err)
	}
	for _, item := range remote.Items {
		found := false
		for _, tag := range item.ScopeTags {
			found = found || tag == "remote"
		}
		if !found || len(item.Versions) == 0 {
			t.Fatalf("filtered item missing tag or versions: %+v", item)
		}
	}
	none, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{Scope: "absent"})
	if err != nil || len(none.Items) != 0 || none.NextCursor != "" {
		t.Fatalf("empty scope: %+v %v", none, err)
	}
	first, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || second.NextCursor != "" {
		t.Fatalf("second page: %+v %v", second, err)
	}
	seen := map[string]bool{}
	for _, item := range append(first.Items, second.Items...) {
		if seen[item.ID] || !ids[item.ID] {
			t.Fatalf("page overlap or unknown id: %+v", item)
		}
		seen[item.ID] = true
	}
	if _, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{Limit: 2, Cursor: first.NextCursor, Scope: "remote"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cursor with changed filter: %v", err)
	}
	if _, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{Cursor: "bogus"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bogus cursor: %v", err)
	}
	if _, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{Limit: 101}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlong limit: %v", err)
	}
	all, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{})
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("default list: %+v %v", all, err)
	}
}

func TestSavedAnswerReadNotFound(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	if _, err := s.SavedAnswer(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty id: %v", err)
	}
	if _, err := s.SavedAnswer(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: %v", err)
	}
}

func TestSavedAnswerReadVerifiesTextHash(t *testing.T) {
	ctx := context.Background()
	s := openJobTestStore(t)
	value, _, err := s.CreateSavedAnswer(ctx, ownerActor(), SavedAnswerCreateInput{RequestKey: "hash-create",
		Text: "Checked wording.", ScopeTags: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE saved_answer_versions SET text='tampered'
	  WHERE answer_id=? AND version=1`, value.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavedAnswer(ctx, value.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered text accepted: %v", err)
	}
	if _, err := s.ListSavedAnswers(ctx, SavedAnswerListOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered text listed: %v", err)
	}
}
