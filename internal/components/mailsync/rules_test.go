package mailsync

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func waitRule(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("rule worker did not finish")
}
func sampleRule() Rule {
	return Rule{ID: "test-rule", Enabled: true, Conditions: []RuleCondition{{Field: "from", Match: "ends", Value: "@example.com"}, {Field: "subject", Match: "contains", Value: "passed"}}, Actions: []RuleAction{{Action: "move", Target: "Archive"}, {Action: "read"}, {Action: "flag", Target: "green"}}}
}
func TestRulesValidateMatchAndOrder(t *testing.T) {
	rule := sampleRule()
	m := Metadata{Key: "key", Email: imap.Email{From: "Build <build@example.com>", To: []string{"me@icloud.com"}, Subject: "BUILD Passed", ListID: "Notes <fieldnotes.email>"}}
	if err := rule.validate(); err != nil {
		t.Fatal(err)
	}
	if !rule.matches(m) {
		t.Fatal("AND conditions did not match")
	}
	m.Subject = "failed"
	if rule.matches(m) {
		t.Fatal("ignored second condition")
	}
	m.Subject = "BUILD passed"
	rule.Conditions = []RuleCondition{{Field: "list-id", Match: "equals", Value: "fieldnotes.email"}}
	if !rule.matches(m) {
		t.Fatal("List-ID not normalized")
	}
	legacy := Rule{ID: "legacy", Field: "to", Match: "equals", Value: "ME@icloud.com", Action: "forward", Target: "elsewhere@example.com", Enabled: true}
	if err := legacy.validate(); err != nil || !legacy.matches(m) {
		t.Fatal("legacy rule failed", err)
	}
	actions := ruleActions([]Rule{sampleRule(), legacy}, m)
	if len(actions) != 4 || actions[3].Action != "move" || actions[2].Action != "forward" || actions[2].Key == "" {
		t.Fatal("destination not deferred", actions)
	}
	m.AutoSubmitted = "auto-generated"
	if len(ruleActions([]Rule{legacy}, m)) != 0 {
		t.Fatal("automated forwarding loop allowed")
	}
	for _, bad := range []Rule{{ID: "x"}, {ID: "x", Conditions: []RuleCondition{{Field: "from", Match: "regex", Value: ".*"}}, Actions: []RuleAction{{Action: "read"}}}, {ID: "x", Conditions: legacy.conditions(), Actions: []RuleAction{{Action: "forward", Target: "bad\r\nBcc:bad@example.com"}}}, {ID: "x", Conditions: legacy.conditions(), Actions: []RuleAction{{Action: "flag", Target: "pink"}}}, {ID: "x", Conditions: legacy.conditions(), Actions: []RuleAction{{Action: "move", Target: "Archive"}, {Action: "archive"}}}} {
		if bad.validate() == nil {
			t.Fatal("accepted invalid rule", bad)
		}
	}
	p := defaultPreferences()
	p.Rules = []Rule{legacy, legacy}
	if p.validate() == nil {
		t.Fatal("accepted duplicate rule IDs")
	}
}

type ruleWire struct{ *moveWire }

func (f *ruleWire) FlagEmail(_ context.Context, folder, id, kind, color string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind != "follow-up" {
		return errors.New("invalid flag type")
	}
	m, ok := f.mail[folder][id]
	if !ok {
		return errors.New("missing message")
	}
	m.FlagColor = color
	f.mail[folder][id] = m
	return nil
}
func (f *ruleWire) GetEmail(_ context.Context, folder, id string) (*imap.Email, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.mail[folder][id]
	if !ok {
		return nil, errors.New("missing message")
	}
	return &m, nil
}
func TestArrivalRulesDurableActionsAndIdempotentForward(t *testing.T) {
	s := testMirror(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := &moveWire{mail: map[string]map[string]imap.Email{"INBOX": {"1": {ID: "1", From: "build@example.com", Subject: "passed before enabling", Date: time.Now(), Unread: true}}, "Archive": {}}, next: map[string]uint32{"INBOX": 2, "Archive": 1}}
	connectMoveFake(s, f)
	s.mu.Lock()
	s.dialCommand = func(_ context.Context, address, _ string) (*Connection, error) {
		return &Connection{Mail: &ruleWire{f}, Address: address}, nil
	}
	s.mu.Unlock()
	if _, err := s.Start(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitReady(ctx, "owner"); err != nil {
		t.Fatal(err)
	}
	p := defaultPreferences()
	rule := sampleRule()
	rule.Actions = append([]RuleAction{{Action: "forward", Target: "forward@example.com"}}, rule.Actions...)
	p.Rules = []Rule{rule}
	p.UndoSeconds = 30
	if err := s.SavePreferences(ctx, "owner", p); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.mail["INBOX"]["2"] = imap.Email{ID: "2", From: "build@example.com", Subject: "passed after enabling", BodyPlain: "Full original body", Date: time.Now(), Unread: true}
	f.next["INBOX"] = 3
	f.mu.Unlock()
	if err := s.Refresh(ctx, "owner", "imap", "", "INBOX"); err != nil {
		t.Fatal(err)
	}
	waitRule(t, func() bool {
		n, _ := s.db.Collection("mail_rule_jobs").CountDocuments(ctx, bson.M{"owner": "owner", "state": "done"})
		return n == 1
	})
	f.mu.Lock()
	initial := f.mail["INBOX"]["1"]
	remaining := len(f.mail["INBOX"])
	moved := f.mail["Archive"]["1"]
	f.mu.Unlock()
	if !initial.Unread || remaining != 1 || moved.Unread || moved.FlagColor != "green" {
		t.Fatal("rules ran on history or lost an action", initial, moved)
	}
	jobs, err := s.Outbox(ctx, "owner")
	if err != nil || len(jobs) != 1 || !jobs[0].Message.Automated || jobs[0].Actor != "rule" || jobs[0].Message.Body == "" {
		t.Fatal("forward not durably queued", jobs, err)
	}
	var stored RuleJob
	if err = s.db.Collection("mail_rule_jobs").FindOne(ctx, bson.M{"state": "done"}).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if err = s.ruleForward(ctx, stored, stored.Actions[0]); err != nil {
		t.Fatal("forward retry fetched already moved mail", err)
	}
	jobs, err = s.Outbox(ctx, "owner")
	if err != nil || len(jobs) != 1 {
		t.Fatal("forward retry duplicated send")
	}
	preview, err := s.PreviewRule(ctx, "owner", rule)
	if err != nil || preview.Scanned != 1 || preview.Matched != 1 {
		t.Fatal("dry run mismatch", preview, err)
	}
	status, err := s.RulesStatus(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyRules(ctx, "owner", "stale"); err == nil {
		t.Fatal("applied stale rules")
	}
	application, err := s.ApplyRules(ctx, "owner", status.Digest)
	if err != nil {
		t.Fatal(err)
	}
	waitRule(t, func() bool {
		var record RuleApplication
		e := s.db.Collection("mail_rule_applications").FindOne(ctx, bson.M{"_id": application.ID}).Decode(&record)
		return e == nil && record.State == "done" && record.Matched == 1
	})
	waitRule(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.mail["INBOX"]) == 0 })
	foreign, err := s.RulesStatus(ctx, "other")
	if err != nil || len(foreign.Jobs) != 0 || len(foreign.Applications) != 0 {
		t.Fatal("foreign rules visible", err)
	}
}
func TestRuleApplyPagesAndJournalRecovery(t *testing.T) {
	s := testMirror(t)
	s.cancel()
	s.wg.Wait()
	ctx := context.Background()
	owner, account := "owner", "mirror@icloud.com"
	p := defaultPreferences()
	rule := sampleRule()
	rule.Actions = []RuleAction{{Action: "read"}}
	p.Rules = []Rule{rule}
	if err := s.SavePreferences(ctx, owner, p); err != nil {
		t.Fatal(err)
	}
	folder := Folder{Key: identity(owner, account, "INBOX"), Owner: owner, Account: account, FolderInfo: imap.FolderInfo{Name: "INBOX", Role: "inbox", UIDValidity: 7, UIDNext: 502}, Ready: true, Complete: true}
	changes := []Change{{Entity: "folder", Op: "put", Key: folder.Key, Folder: &folder}}
	for i := 1; i <= 501; i++ {
		m := Metadata{Owner: owner, Account: account, Email: imap.Email{ID: fmt.Sprint(i), Folder: "INBOX", UIDValidity: 7, From: "build@example.com", Subject: "passed", Date: time.Now()}}
		m.Key = messageKey(owner, account, "INBOX", 7, m.ID)
		changes = append(changes, Change{Entity: "msg", Op: "put", Key: m.Key, Message: &m})
	}
	if err := s.commit(ctx, owner, account, "imap", "", changes); err != nil {
		t.Fatal(err)
	}
	preview, err := s.PreviewRule(ctx, owner, rule)
	if err != nil || preview.Scanned != 500 || preview.Matched != 500 {
		t.Fatal(preview, err)
	}
	application, err := s.ApplyRules(ctx, owner, ruleDigest(p.Rules))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = s.runRuleApplication(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var applied RuleApplication
	if err = s.db.Collection("mail_rule_applications").FindOne(ctx, bson.M{"_id": application.ID}).Decode(&applied); err != nil || applied.State != "done" || applied.Scanned != 501 || applied.Matched != 501 {
		t.Fatal(applied, err)
	}
	count, err := s.db.Collection("mail_rule_jobs").CountDocuments(ctx, bson.M{"owner": owner})
	if err != nil || count != 501 {
		t.Fatal("page skipped jobs", count, err)
	}
	var job RuleJob
	if err = s.db.Collection("mail_rule_jobs").FindOne(ctx, bson.M{"owner": owner}).Decode(&job); err != nil {
		t.Fatal(err)
	}
	job.Step = 1
	job.State = "done"
	if _, err = s.db.Collection("mail_rule_jobs").ReplaceOne(ctx, bson.M{"_id": job.ID}, job); err != nil {
		t.Fatal(err)
	}
	job.State = "pending"
	job.Step = 0
	entry := Change{Owner: owner, Account: account, Entity: "rule-job", Op: "put", Key: job.ID, RuleJob: &job}
	if err = s.applyChanges(ctx, []Change{entry}); err != nil {
		t.Fatal(err)
	}
	var replay RuleJob
	if err = s.db.Collection("mail_rule_jobs").FindOne(ctx, bson.M{"_id": job.ID}).Decode(&replay); err != nil || replay.State != "done" || replay.Step != 1 {
		t.Fatal("journal replay reset completed action", replay, err)
	}
	entry.Owner = "foreign"
	if err = s.applyChanges(ctx, []Change{entry}); err == nil {
		t.Fatal("foreign journal job accepted")
	}
}

func TestDisabledRulesConsumeOldJobsWithoutApplying(t *testing.T) {
	s := testMirror(t)
	s.cancel()
	s.wg.Wait()
	ctx := context.Background()
	p := defaultPreferences()
	rule := sampleRule()
	p.Rules = []Rule{rule}
	if err := s.SavePreferences(ctx, "owner", p); err != nil {
		t.Fatal(err)
	}
	m := Metadata{Key: "old-arrival", Owner: "owner", Account: "mirror@icloud.com", Email: imap.Email{ID: "1", Folder: "INBOX", UIDValidity: 7, From: "build@example.com", Subject: "passed"}}
	job := newRuleJob("owner", m.Account, p.Rules, m)
	if err := s.insertRuleJobs(ctx, []RuleJob{*job}); err != nil {
		t.Fatal(err)
	}
	p.Rules[0].Enabled = false
	if err := s.SavePreferences(ctx, "owner", p); err != nil {
		t.Fatal(err)
	}
	if err := s.advanceRule(ctx, *job); err != nil {
		t.Fatal("disabled rule attempted an IMAP connection", err)
	}
	var stored RuleJob
	if err := s.db.Collection("mail_rule_jobs").FindOne(ctx, bson.M{"_id": job.ID}).Decode(&stored); err != nil || stored.State != "done" || stored.Step != 3 {
		t.Fatal("disabled work remained pending", stored, err)
	}
}
