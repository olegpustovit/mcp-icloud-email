package mailsync

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/olegpustovit/mcp-icloud-email/imap"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type threadNode struct {
	Key        string `bson:"_id"`
	Owner      string `bson:"owner"`
	Account    string `bson:"account"`
	Identifier string `bson:"identifier"`
	Thread     string `bson:"thread"`
}

type threadUnion map[string]string

func (u threadUnion) root(id string) string {
	parent, ok := u[id]
	if !ok {
		u[id] = id
		return id
	}
	if parent != id {
		u[id] = u.root(parent)
	}
	return u[id]
}
func (u threadUnion) join(a, b string) {
	a = u.root(a)
	b = u.root(b)
	if a < b {
		u[b] = a
	} else {
		u[a] = b
	}
}

// Batch reference containers keep child-before-parent imports and cycles stable.
// Metadata and nodes are read/written in batches, including during initial import.
func (s *Service) prepareBatch(ctx context.Context, owner, account string, emails []imap.Email) ([]Metadata, []Change, error) {
	ids := map[string]string{}
	subjects := []string{}
	anchors := make([]string, len(emails))
	keys := make([]string, len(emails))
	oldRoots := map[string]bool{}
	union := threadUnion{}
	earliest, latest := time.Now().UTC(), time.Time{}
	for i := range emails {
		email := &emails[i]
		email.BodyPlain = ""
		email.BodyHTML = ""
		email.Date = email.Date.UTC()
		email.Flags = slices.Clone(email.Flags)
		slices.Sort(email.Flags)
		email.MessageID = strings.Trim(strings.TrimSpace(email.MessageID), "<>")
		keys[i] = messageKey(owner, account, email.Folder, email.UIDValidity, email.ID)
		anchors[i] = identity(owner, account, keys[i])
		if email.MessageID != "" {
			anchors[i] = identity(owner, account, email.MessageID)
			ids[email.MessageID] = anchors[i]
		}
		union.root(anchors[i])
		for n, reference := range email.References {
			reference = strings.Trim(strings.TrimSpace(reference), "<>")
			email.References[n] = reference
			if reference != "" {
				node := identity(owner, account, reference)
				ids[reference] = node
				union.join(anchors[i], node)
			}
		}
		if subject := normalizeSubject(email.Subject); subject != "" {
			subjects = append(subjects, identity(subject))
		}
		if email.Date.Before(earliest) {
			earliest = email.Date
		}
		if email.Date.After(latest) {
			latest = email.Date
		}
	}
	nodeKeys := make([]string, 0, len(ids))
	identifiers := make([]string, 0, len(ids))
	for id, key := range ids {
		nodeKeys = append(nodeKeys, key)
		identifiers = append(identifiers, id)
	}
	for start := 0; start < len(nodeKeys); start += 5000 {
		rows, err := s.db.Collection("mail_thread_nodes").Find(ctx, bson.M{"owner": owner, "account": account, "_id": bson.M{"$in": nodeKeys[start:min(start+5000, len(nodeKeys))]}})
		if err != nil {
			return nil, nil, err
		}
		var nodes []threadNode
		err = rows.All(ctx, &nodes)
		_ = rows.Close(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, node := range nodes {
			union.join(node.Key, node.Thread)
			oldRoots[node.Thread] = true
		}
	}
	// Bridge metadata also repairs an interrupted import that already updated nodes.
	filter := bson.M{"owner": owner, "account": account, "$or": []bson.M{{"_id": bson.M{"$in": keys}}, {"email.messageid": bson.M{"$in": identifiers}}, {"subject_key": bson.M{"$in": subjects}, "email.date": bson.M{"$gte": earliest.AddDate(0, 0, -30), "$lte": latest.AddDate(0, 0, 30)}}}}
	rows, err := s.db.Collection("mail_messages").Find(ctx, filter, options.Find().SetProjection(bson.M{"email.threadid": 1, "email.messageid": 1, "email.date": 1, "subject_key": 1}))
	if err != nil {
		return nil, nil, err
	}
	var peers []Metadata
	err = rows.All(ctx, &peers)
	_ = rows.Close(ctx)
	if err != nil {
		return nil, nil, err
	}
	bySubject := map[string][]Metadata{}
	for _, peer := range peers {
		if peer.ThreadID == "" {
			continue
		}
		oldRoots[peer.ThreadID] = true
		if key, ok := ids[peer.MessageID]; ok {
			union.join(key, peer.ThreadID)
		}
		bySubject[peer.SubjectKey] = append(bySubject[peer.SubjectKey], peer)
	}
	for i, email := range emails {
		subject := identity(normalizeSubject(email.Subject))
		if len(email.References) == 0 && normalizeSubject(email.Subject) != "" {
			for _, peer := range bySubject[subject] {
				if !peer.Date.Before(email.Date.AddDate(0, 0, -30)) && !peer.Date.After(email.Date.AddDate(0, 0, 30)) {
					union.join(anchors[i], peer.ThreadID)
				}
			}
		}
		bySubject[subject] = append(bySubject[subject], Metadata{Email: imap.Email{ThreadID: anchors[i], Date: email.Date}})
	}
	models := []mongo.WriteModel{}
	mergedRoots := []string{}
	rootTargets := map[string]string{}
	for root := range oldRoots {
		target := union.root(root)
		if target != root {
			mergedRoots = append(mergedRoots, root)
			rootTargets[root] = target
			models = append(models, mongo.NewUpdateManyModel().SetFilter(bson.M{"owner": owner, "account": account, "thread": root}).SetUpdate(bson.M{"$set": bson.M{"thread": target}}))
		}
	}
	for id, key := range ids {
		node := threadNode{Key: key, Owner: owner, Account: account, Identifier: id, Thread: union.root(key)}
		models = append(models, mongo.NewReplaceOneModel().SetFilter(bson.M{"_id": key, "owner": owner, "account": account}).SetReplacement(node).SetUpsert(true))
	}
	if len(models) > 0 {
		if _, err = s.db.Collection("mail_thread_nodes").BulkWrite(ctx, models); err != nil {
			return nil, nil, err
		}
	}
	merged := []Change{}
	if len(mergedRoots) > 0 {
		mail, err := s.metadata(ctx, owner, account, bson.M{"email.threadid": bson.M{"$in": mergedRoots}}, 0)
		if err != nil {
			return nil, nil, err
		}
		for _, old := range mail {
			next := old
			next.ThreadID = rootTargets[old.ThreadID]
			next.Fingerprint = metadataFingerprint(next.Email)
			merged = append(merged, Change{Entity: "msg", Op: "put", Key: next.Key, Message: &next})
		}
	}
	prepared := make([]Metadata, len(emails))
	for i, email := range emails {
		email.ThreadID = union.root(anchors[i])
		prepared[i] = Metadata{Key: keys[i], Owner: owner, Account: account, Email: email, SubjectKey: identity(normalizeSubject(email.Subject)), Fingerprint: metadataFingerprint(email)}
	}
	return prepared, merged, nil
}

func (s *Service) Thread(ctx context.Context, owner, account, thread string) ([]Metadata, error) {
	return s.metadata(ctx, owner, account, bson.M{"email.threadid": thread}, 0)
}
