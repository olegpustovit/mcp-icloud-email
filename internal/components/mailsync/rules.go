package mailsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"github.com/olegpustovit/mcp-icloud-email/internal/components/auth"
	"github.com/olegpustovit/mcp-icloud-email/internal/mailmessage"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type RuleCondition struct {
	Field string `json:"field" bson:"field"`
	Match string `json:"match" bson:"match"`
	Value string `json:"value" bson:"value"`
}
type RuleAction struct {
	Action        string `json:"action" bson:"action"`
	Target        string `json:"target,omitempty" bson:"target,omitempty"`
	Key           string `json:"-" bson:"key,omitempty"`
	RuleID        string `json:"-" bson:"rule_id,omitempty"`
	ConditionHash string `json:"-" bson:"condition_hash,omitempty"`
	Index         int    `json:"-" bson:"index,omitempty"`
}

func (r Rule) conditions() []RuleCondition {
	if len(r.Conditions) > 0 {
		return r.Conditions
	}
	return []RuleCondition{{r.Field, r.Match, r.Value}}
}
func (r Rule) actions() []RuleAction {
	if len(r.Actions) > 0 {
		return r.Actions
	}
	return []RuleAction{{Action: r.Action, Target: r.Target}}
}
func (r Rule) validate() error {
	if r.ID == "" || len(r.ID) > 100 || len(r.Conditions) > 8 || len(r.Actions) > 8 {
		return errors.New("invalid rule")
	}
	for _, c := range r.conditions() {
		if !validChoice(c.Field, "from", "to", "subject", "list-id") || !validChoice(c.Match, "contains", "equals", "ends") || strings.TrimSpace(c.Value) == "" || len(c.Value) > 1000 || strings.ContainsAny(c.Value, "\r\n\x00") {
			return errors.New("invalid rule condition")
		}
	}
	moves := 0
	for _, a := range r.actions() {
		if len(a.Target) > 1000 || strings.ContainsAny(a.Target, "\r\n\x00") {
			return errors.New("invalid rule target")
		}
		switch a.Action {
		case "move":
			if !validMailbox(a.Target) {
				return errors.New("choose a destination folder")
			}
			moves++
		case "archive":
			moves++
		case "flag":
			if !validChoice(a.Target, "red", "orange", "yellow", "green", "blue", "purple", "gray") {
				return errors.New("choose a flag color")
			}
		case "read":
		case "forward":
			address, err := mail.ParseAddress(a.Target)
			if err != nil || address.Address != a.Target {
				return errors.New("invalid forwarding address")
			}
		default:
			return errors.New("invalid rule action")
		}
	}
	if moves > 1 {
		return errors.New("a rule may have one destination")
	}
	return nil
}
func (r Rule) matches(m Metadata) bool {
	for _, condition := range r.conditions() {
		var values []string
		switch condition.Field {
		case "from":
			values = []string{mailAddress(m.From)}
		case "to":
			for _, raw := range m.To {
				values = append(values, mailAddress(raw))
			}
		case "subject":
			values = []string{m.Subject}
		case "list-id":
			value := m.ListID
			if open := strings.LastIndex(value, "<"); open >= 0 {
				value = value[open+1:]
			}
			values = []string{strings.TrimSpace(strings.TrimSuffix(value, ">"))}
		}
		matched := false
		want := strings.ToLower(condition.Value)
		for _, raw := range values {
			value := strings.ToLower(raw)
			switch condition.Match {
			case "contains":
				matched = matched || strings.Contains(value, want)
			case "equals":
				matched = matched || value == want
			case "ends":
				matched = matched || strings.HasSuffix(value, want)
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
func ruleDigest(rules []Rule) string { raw, _ := json.Marshal(rules); return identity(string(raw)) }
func conditionHash(rule Rule) string {
	raw, _ := json.Marshal(rule.conditions())
	return identity(string(raw))
}
func ruleActionEnabled(rules []Rule, action RuleAction) bool {
	for _, rule := range rules {
		if rule.ID != action.RuleID || !rule.Enabled || conditionHash(rule) != action.ConditionHash {
			continue
		}
		actions := rule.actions()
		return action.Index >= 0 && action.Index < len(actions) && actions[action.Index].Action == action.Action && actions[action.Index].Target == action.Target
	}
	return false
}
func ruleActions(rules []Rule, m Metadata) []RuleAction {
	var actions []RuleAction
	var destination *RuleAction
	for _, rule := range rules {
		if !rule.Enabled || !rule.matches(m) {
			continue
		}
		for index, action := range rule.actions() {
			if action.Action == "forward" && m.AutoSubmitted != "" && !strings.EqualFold(strings.TrimSpace(m.AutoSubmitted), "no") {
				continue
			}
			action.Key = identity(rule.ID, ruleDigest([]Rule{rule}), strconv.Itoa(index))
			action.RuleID, action.ConditionHash, action.Index = rule.ID, conditionHash(rule), index
			if action.Action == "move" || action.Action == "archive" {
				copy := action
				destination = &copy
			} else {
				actions = append(actions, action)
			}
		}
	}
	// Resolve every header action at the original UID, then move once to the last matching destination.
	if destination != nil {
		actions = append(actions, *destination)
	}
	return actions
}

type RuleJob struct {
	ID          string        `bson:"_id" json:"id"`
	Owner       string        `bson:"owner" json:"-"`
	Account     string        `bson:"account" json:"-"`
	Source      imap.Location `bson:"source" json:"source"`
	SourceKey   string        `bson:"source_key" json:"-"`
	Subject     string        `bson:"subject" json:"subject"`
	Actions     []RuleAction  `bson:"actions" json:"actions"`
	Step        int           `bson:"step" json:"step"`
	State       string        `bson:"state" json:"state"`
	Created     time.Time     `bson:"created" json:"created"`
	NextAttempt time.Time     `bson:"next_attempt" json:"-"`
	Attempts    int           `bson:"attempts" json:"-"`
	Error       string        `bson:"error" json:"error,omitempty"`
	Expires     *time.Time    `bson:"expires,omitempty" json:"-"`
}

func newRuleJob(owner, account string, rules []Rule, m Metadata) *RuleJob {
	actions := ruleActions(rules, m)
	if len(actions) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return &RuleJob{ID: identity(owner, account, "rule", m.Key, ruleDigest(rules)), Owner: owner, Account: account, Source: imap.Location{Folder: m.Folder, ID: m.ID, Validity: m.UIDValidity}, SourceKey: m.Key, Subject: m.Subject, Actions: actions, State: "pending", Created: now, NextAttempt: now}
}
func (s *Service) insertRuleJobs(ctx context.Context, jobs []RuleJob) error {
	if len(jobs) == 0 {
		return nil
	}
	writes := make([]mongo.WriteModel, len(jobs))
	for i, job := range jobs {
		writes[i] = mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": job.ID, "owner": job.Owner, "account": job.Account}).SetUpdate(bson.M{"$setOnInsert": job}).SetUpsert(true)
	}
	_, err := s.db.Collection("mail_rule_jobs").BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(true))
	return err
}

type RulePreview struct {
	Scanned int `json:"scanned"`
	Matched int `json:"matched"`
}

func (s *Service) PreviewRule(ctx context.Context, owner string, rule Rule) (RulePreview, error) {
	if err := rule.validate(); err != nil {
		return RulePreview{}, err
	}
	account, err := s.Account(ctx, owner)
	if err != nil {
		return RulePreview{}, err
	}
	folders, err := s.Folders(ctx, owner, account)
	if err != nil {
		return RulePreview{}, err
	}
	inbox := "INBOX"
	for _, f := range folders {
		if f.Role == "inbox" {
			inbox = f.Name
		}
	}
	rows, err := s.metadata(ctx, owner, account, bson.M{"email.folder": inbox}, 500)
	if err != nil {
		return RulePreview{}, err
	}
	preview := RulePreview{Scanned: len(rows)}
	for _, m := range rows {
		if rule.matches(m) {
			preview.Matched++
		}
	}
	return preview, nil
}

type RuleApplication struct {
	ID       string     `bson:"_id" json:"id"`
	Owner    string     `bson:"owner" json:"-"`
	Account  string     `bson:"account" json:"-"`
	Rules    []Rule     `bson:"rules" json:"-"`
	Folder   string     `bson:"folder" json:"folder"`
	Validity uint32     `bson:"validity" json:"-"`
	MaxUID   uint32     `bson:"max_uid" json:"-"`
	After    string     `bson:"after" json:"-"`
	Scanned  int        `bson:"scanned" json:"scanned"`
	Matched  int        `bson:"matched" json:"matched"`
	State    string     `bson:"state" json:"state"`
	Error    string     `bson:"error" json:"error,omitempty"`
	Created  time.Time  `bson:"created" json:"created"`
	Expires  *time.Time `bson:"expires,omitempty" json:"-"`
}

func (s *Service) ApplyRules(ctx context.Context, owner, digest string) (RuleApplication, error) {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return RuleApplication{}, err
	}
	p, err := s.Preferences(ctx, owner)
	if err != nil {
		return RuleApplication{}, err
	}
	if ruleDigest(p.Rules) != digest {
		return RuleApplication{}, errors.New("rules changed; refresh before applying")
	}
	folders, err := s.Folders(ctx, owner, account)
	if err != nil {
		return RuleApplication{}, err
	}
	var inbox Folder
	for _, f := range folders {
		if f.Role == "inbox" || strings.EqualFold(f.Name, "INBOX") {
			inbox = f
			break
		}
	}
	if inbox.UIDValidity == 0 || !inbox.Complete {
		return RuleApplication{}, errors.New("wait for Inbox history to finish importing")
	}
	now := time.Now().UTC()
	job := RuleApplication{ID: identity(owner, account, "apply", digest, inbox.Name, fmt.Sprint(inbox.UIDValidity), fmt.Sprint(inbox.UIDNext)), Owner: owner, Account: account, Rules: p.Rules, Folder: inbox.Name, Validity: inbox.UIDValidity, MaxUID: inbox.UIDNext - 1, State: "pending", Created: now}
	_, err = s.db.Collection("mail_rule_applications").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$setOnInsert": job}, options.UpdateOne().SetUpsert(true))
	return job, err
}
func (s *Service) runRuleApplication(ctx context.Context) error {
	var job RuleApplication
	err := s.db.Collection("mail_rule_applications").FindOne(ctx, bson.M{"state": "pending"}, options.FindOne().SetSort(bson.D{{Key: "created", Value: 1}})).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return err
	}
	state := s.owner(job.Owner)
	state.write.Lock()
	defer state.write.Unlock()
	if err = s.db.Collection("mail_rule_applications").FindOne(ctx, bson.M{"_id": job.ID, "state": "pending"}).Decode(&job); errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	} else if err != nil {
		return err
	}
	account, err := s.Account(ctx, job.Owner)
	if err != nil {
		return err
	}
	if account != job.Account {
		return s.finishRuleApplication(ctx, job, "cancelled", "Linked account changed.")
	}
	if err = s.checkRenamePending(ctx, job.Owner, job.Account); err != nil {
		return nil
	}
	var folder Folder
	err = s.db.Collection("mail_folders").FindOne(ctx, bson.M{"owner": job.Owner, "account": job.Account, "info.name": job.Folder}).Decode(&folder)
	if err != nil || folder.UIDValidity != job.Validity {
		return s.finishRuleApplication(ctx, job, "failed", "Inbox generation changed; run again after syncing.")
	}
	rows, err := s.db.Collection("mail_messages").Find(ctx, bson.M{"owner": job.Owner, "account": job.Account, "email.folder": job.Folder, "email.uidvalidity": job.Validity, "_id": bson.M{"$gt": job.After}}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(200))
	if err != nil {
		return err
	}
	var messages []Metadata
	err = rows.All(ctx, &messages)
	_ = rows.Close(ctx)
	if err != nil {
		return err
	}
	var jobs []RuleJob
	for _, m := range messages {
		job.After = m.Key
		uid, _ := strconv.ParseUint(m.ID, 10, 32)
		if uid > uint64(job.MaxUID) {
			continue
		}
		job.Scanned++
		if next := newRuleJob(job.Owner, job.Account, job.Rules, m); next != nil {
			jobs = append(jobs, *next)
			job.Matched++
		}
	}
	if err = s.insertRuleJobs(ctx, jobs); err != nil {
		return err
	}
	if len(messages) < 200 {
		job.State = "done"
		expires := time.Now().Add(14 * 24 * time.Hour)
		job.Expires = &expires
	}
	_, err = s.db.Collection("mail_rule_applications").ReplaceOne(ctx, bson.M{"_id": job.ID, "state": "pending"}, job)
	return err
}
func (s *Service) finishRuleApplication(ctx context.Context, job RuleApplication, state, problem string) error {
	_, err := s.db.Collection("mail_rule_applications").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"state": state, "error": problem, "expires": time.Now().Add(14 * 24 * time.Hour)}})
	return err
}
func (s *Service) ruleForward(ctx context.Context, job RuleJob, action RuleAction) error {
	tx := "rule:" + identity(job.SourceKey, action.Key)
	// Check the durable send receipt before fetching mail that a later action may already have moved.
	var existing Outbox
	err := s.db.Collection("mail_outbox").FindOne(ctx, bson.M{"_id": auth.Hash(job.Owner + "\x00" + tx), "owner": job.Owner, "account": job.Account}).Decode(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}
	if strings.EqualFold(action.Target, job.Account) {
		return errors.New("a forwarding rule cannot target the linked account")
	}
	connection, release, err := s.Acquire(ctx, job.Owner)
	if err != nil {
		return err
	}
	var original *imap.Email
	var files []mailmessage.Attachment
	err = func() error {
		defer release()
		if connection.Address != job.Account {
			return errors.New("linked account changed")
		}
		if err := s.checkRenamePending(ctx, job.Owner, job.Account); err != nil {
			return err
		}
		validity, err := connection.Mail.UIDValidity(ctx, job.Source.Folder)
		if err != nil {
			return err
		}
		if validity != job.Source.Validity {
			return errors.New("mailbox generation changed")
		}
		original, err = connection.Mail.GetEmail(ctx, job.Source.Folder, job.Source.ID)
		if err != nil {
			return err
		}
		var bytes int64
		for _, file := range original.Attachments {
			bytes += file.Size
		}
		if bytes > mailmessage.MaxAttachments || len(original.Attachments) > 30 {
			return errors.New("forward attachments exceed 10 MB")
		}
		for _, file := range original.Attachments {
			part, err := connection.Mail.GetAttachment(ctx, job.Source.Folder, job.Source.ID, file.Filename)
			if err != nil {
				return err
			}
			kind := part.MIMEType
			if kind == "" {
				kind = http.DetectContentType(part.Content)
			}
			files = append(files, mailmessage.Attachment{Filename: part.Filename, MIMEType: kind, Content: part.Content})
			if err = mailmessage.ValidateAttachments(files); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return err
	}
	body := "---------- Forwarded message ----------\nFrom: " + original.From + "\nDate: " + original.Date.Format(time.RFC1123) + "\nSubject: " + original.Subject + "\nTo: " + strings.Join(original.To, ", ") + "\n\n" + original.BodyPlain
	rich := original.BodyHTML != ""
	if rich {
		body = "<pre>" + html.EscapeString(strings.SplitN(body, "\n\n", 2)[0]) + "</pre>" + original.BodyHTML
	}
	_, err = s.schedule(ctx, job.Owner, Send{To: []string{action.Target}, Subject: "Fwd: " + original.Subject, Body: body, HTML: rich, Attachments: files, TransactionID: tx, Automated: true}, "rule")
	return err
}
func (s *Service) advanceRule(ctx context.Context, job RuleJob) error {
	account, err := s.Account(ctx, job.Owner)
	if err != nil {
		return err
	}
	if account != job.Account {
		return errors.New("linked account changed")
	}
	if err = s.checkRenamePending(ctx, job.Owner, job.Account); err != nil {
		return err
	}
	box := &Mailbox{s: s, owner: job.Owner, account: job.Account, actor: "rule"}
	for job.Step < len(job.Actions) {
		if err := s.db.Collection("mail_rule_jobs").FindOne(ctx, bson.M{"_id": job.ID, "owner": job.Owner, "account": job.Account, "state": "pending"}).Decode(&job); err != nil {
			return err
		}
		action := job.Actions[job.Step]
		prefs, err := s.Preferences(ctx, job.Owner)
		if err != nil {
			return err
		}
		tx := job.ID + ":" + strconv.Itoa(job.Step)
		actionCtx := Transaction(ctx, tx)
		// Disabled, deleted or edited rules cannot start an old queued action.
		if ruleActionEnabled(prefs.Rules, action) {
			switch action.Action {
			case "forward":
				err = s.ruleForward(ctx, job, action)
			case "move", "archive":
				target := action.Target
				if action.Action == "archive" {
					folders, e := s.Folders(ctx, job.Owner, job.Account)
					if e != nil {
						return e
					}
					for _, f := range folders {
						if f.Role == "archive" {
							target = f.Name
							break
						}
					}
					if target == "" {
						return errors.New("archive folder unavailable")
					}
				}
				if target != job.Source.Folder {
					err = box.MoveWithValidity(actionCtx, job.Source, target)
				}
			case "read", "flag":
				err = box.change(actionCtx, []string{job.Source.Folder}, func(c *Connection) error {
					validity, e := c.Mail.UIDValidity(ctx, job.Source.Folder)
					if e != nil {
						return e
					}
					if validity != job.Source.Validity {
						return errors.New("mailbox generation changed")
					}
					if action.Action == "read" {
						return c.Mail.MarkRead(ctx, job.Source.Folder, job.Source.ID, true)
					}
					return c.Mail.FlagEmail(ctx, job.Source.Folder, job.Source.ID, "follow-up", action.Target)
				})
			}
		}
		if err != nil {
			return err
		}
		job.Step++
		update := bson.M{"step": job.Step, "error": ""}
		if job.Step == len(job.Actions) {
			update["state"] = "done"
			update["expires"] = time.Now().Add(14 * 24 * time.Hour)
		}
		if _, err = s.db.Collection("mail_rule_jobs").UpdateOne(ctx, bson.M{"_id": job.ID, "owner": job.Owner, "account": job.Account, "state": "pending"}, bson.M{"$set": update}); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) runRuleJob(ctx context.Context) error {
	var job RuleJob
	err := s.db.Collection("mail_rule_jobs").FindOne(ctx, bson.M{"state": "pending", "next_attempt": bson.M{"$lte": time.Now()}}, options.FindOne().SetSort(bson.D{{Key: "created", Value: 1}})).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.advanceRule(ctx, job); err != nil {
		if errors.Is(err, ErrRenamePending) {
			_, e := s.db.Collection("mail_rule_jobs").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"next_attempt": time.Now().Add(10 * time.Second), "error": "Paused until folder rename recovery completes."}})
			return e
		}
		job.Attempts++
		state := "pending"
		if job.Attempts >= 5 {
			state = "failed"
		}
		update := bson.M{"state": state, "error": cutText(err.Error(), 500), "attempts": job.Attempts, "next_attempt": time.Now().Add(time.Duration(job.Attempts) * time.Minute)}
		if state == "failed" {
			update["expires"] = time.Now().Add(14 * 24 * time.Hour)
		}
		_, e := s.db.Collection("mail_rule_jobs").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": update})
		return e
	}
	return nil
}
func (s *Service) runRules() {
	defer s.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if err := s.runRuleApplication(s.ctx); err != nil {
				continue
			}
			_ = s.runRuleJob(s.ctx)
		}
	}
}

type RulesStatus struct {
	Digest       string            `json:"digest"`
	Jobs         []RuleJob         `json:"jobs"`
	Applications []RuleApplication `json:"applications"`
}

func (s *Service) RulesStatus(ctx context.Context, owner string) (RulesStatus, error) {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return RulesStatus{}, err
	}
	p, err := s.Preferences(ctx, owner)
	if err != nil {
		return RulesStatus{}, err
	}
	status := RulesStatus{Digest: ruleDigest(p.Rules), Jobs: []RuleJob{}, Applications: []RuleApplication{}}
	rows, err := s.db.Collection("mail_rule_jobs").Find(ctx, bson.M{"owner": owner, "account": account, "state": bson.M{"$in": []string{"pending", "failed"}}}, options.Find().SetSort(bson.D{{Key: "created", Value: -1}}).SetLimit(30))
	if err != nil {
		return status, err
	}
	err = rows.All(ctx, &status.Jobs)
	_ = rows.Close(ctx)
	if err != nil {
		return status, err
	}
	rows, err = s.db.Collection("mail_rule_applications").Find(ctx, bson.M{"owner": owner, "account": account}, options.Find().SetSort(bson.D{{Key: "created", Value: -1}}).SetLimit(5))
	if err != nil {
		return status, err
	}
	err = rows.All(ctx, &status.Applications)
	_ = rows.Close(ctx)
	return status, err
}
func (s *Service) RetryRuleJob(ctx context.Context, owner, id string) error {
	account, err := s.Account(ctx, owner)
	if err != nil {
		return err
	}
	_, err = s.db.Collection("mail_rule_jobs").UpdateOne(ctx, bson.M{"_id": id, "owner": owner, "account": account, "state": "failed"}, bson.M{"$set": bson.M{"state": "pending", "attempts": 0, "error": "", "next_attempt": time.Now()}, "$unset": bson.M{"expires": ""}})
	return err
}
