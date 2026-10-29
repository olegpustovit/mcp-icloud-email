package auth

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type ClientGrant struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Granted  time.Time `json:"granted"`
	LastUsed time.Time `json:"lastUsed"`
}

func (s *Service) Clients(ctx context.Context, owner string) ([]ClientGrant, error) {
	rows, err := s.db.Collection("auth").Find(ctx, bson.M{"email": owner, "kind": bson.M{"$in": []string{"access", "refresh"}}, "expires": bson.M{"$gt": time.Now()}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	var records []Record
	if err = rows.All(ctx, &records); err != nil {
		return nil, err
	}
	grants := map[string]ClientGrant{}
	for _, record := range records {
		if record.ClientID == "" {
			continue
		}
		grant := grants[record.ClientID]
		grant.ID = record.ClientID
		if !record.Created.IsZero() && (grant.Granted.IsZero() || record.Created.Before(grant.Granted)) {
			grant.Granted = record.Created
		}
		if record.LastUsed.After(grant.LastUsed) {
			grant.LastUsed = record.LastUsed
		}
		grants[grant.ID] = grant
	}
	result := make([]ClientGrant, 0, len(grants))
	for _, grant := range grants {
		var registration Record
		if err = s.db.Collection("auth").FindOne(ctx, bson.M{"_id": Hash(grant.ID), "kind": "client"}).Decode(&registration); err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, err
		}
		var data struct {
			Name string `json:"client_name"`
		}
		_ = json.Unmarshal([]byte(registration.Data), &data)
		grant.Name = data.Name
		if grant.Name == "" {
			grant.Name = "MCP client " + grant.ID[:min(8, len(grant.ID))]
		}
		result = append(result, grant)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LastUsed.After(result[j].LastUsed) })
	return result, nil
}
func (s *Service) RevokeClient(ctx context.Context, owner, id string) error {
	_, err := s.db.Collection("auth").DeleteMany(ctx, bson.M{"email": owner, "client_id": id, "kind": bson.M{"$in": []string{"access", "refresh", "code"}}})
	return err
}

type BrowserSession struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Current bool      `json:"current"`
}

func (s *Service) ActiveSession(ctx context.Context, owner, hash string) (bool, error) {
	count, err := s.db.Collection("auth").CountDocuments(ctx, bson.M{"_id": hash, "kind": "session", "email": owner, "expires": bson.M{"$gt": time.Now()}})
	return count == 1, err
}

func (s *Service) Sessions(ctx context.Context, owner, current string) ([]BrowserSession, error) {
	rows, err := s.db.Collection("auth").Find(ctx, bson.M{"email": owner, "kind": "session", "expires": bson.M{"$gt": time.Now()}})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close(ctx) }()
	var records []Record
	if err = rows.All(ctx, &records); err != nil {
		return nil, err
	}
	result := make([]BrowserSession, 0, len(records))
	for _, record := range records {
		result = append(result, BrowserSession{ID: record.ID, Agent: record.Data, Created: record.Created, Expires: record.Expires, Current: record.ID == Hash(current)})
	}
	return result, nil
}
func (s *Service) RevokeOtherSessions(ctx context.Context, owner, current string) error {
	_, err := s.db.Collection("auth").DeleteMany(ctx, bson.M{"email": owner, "kind": "session", "_id": bson.M{"$ne": Hash(current)}})
	return err
}
