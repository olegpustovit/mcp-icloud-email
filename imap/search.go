package imap

import (
	"errors"
	"regexp"
	"strings"
	"time"

	imapv1 "github.com/emersion/go-imap"
)

type searchTerm struct{ key, value string }
type Query struct{ terms []searchTerm }

var queryTokens = regexp.MustCompile(`(?:[^\s"]+|"[^"]*")+`)

func ParseQuery(text string) (Query, error) {
	q := Query{}
	if len(text) > 1000 || strings.ContainsAny(text, "\x00\r\n") {
		return q, errors.New("invalid search query")
	}
	for _, token := range queryTokens.FindAllString(text, -1) {
		key, value, operator := strings.Cut(token, ":")
		if !operator {
			key, value = "", key
		}
		key = strings.ToLower(key)
		value = strings.Trim(value, `"`)
		if key == "is" || key == "has" {
			value = strings.ToLower(value)
		}
		switch key {
		case "after", "before":
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return q, errors.New("search dates must use YYYY-MM-DD")
			}
		case "is":
			if value != "read" && value != "unread" && value != "flagged" {
				return q, errors.New("unknown is: search filter")
			}
		case "has":
			if value != "attachment" {
				return q, errors.New("unknown has: search filter")
			}
		}
		if value != "" {
			q.terms = append(q.terms, searchTerm{key, value})
		}
	}
	return q, nil
}
func (q Query) apply(criteria *imapv1.SearchCriteria) {
	for _, term := range q.terms {
		switch term.key {
		case "from", "to", "subject":
			criteria.Header.Add(term.key, term.value)
		case "is":
			switch term.value {
			case "read":
				criteria.WithFlags = append(criteria.WithFlags, imapv1.SeenFlag)
			case "unread":
				criteria.WithoutFlags = append(criteria.WithoutFlags, imapv1.SeenFlag)
			case "flagged":
				criteria.WithFlags = append(criteria.WithFlags, imapv1.FlaggedFlag)
			}
		case "in", "has", "after", "before":
		default:
			criteria.Text = append(criteria.Text, term.value)
		}
	}
}
func (q Query) IncludesFolder(name, role string) bool {
	scoped := false
	for _, term := range q.terms {
		if term.key == "in" {
			scoped = true
			if term.value != "all" && term.value != "anywhere" && !strings.EqualFold(term.value, name) && !strings.EqualFold(term.value, role) {
				return false
			}
		}
	}
	return scoped || (role != "trash" && role != "junk")
}
func (q Query) MatchesMetadata(mail Email) bool {
	for _, term := range q.terms {
		contains := func(value string) bool { return strings.Contains(strings.ToLower(value), strings.ToLower(term.value)) }
		switch term.key {
		case "from":
			if !contains(mail.From) {
				return false
			}
		case "to":
			if !contains(strings.Join(mail.To, " ")) {
				return false
			}
		case "subject":
			if !contains(mail.Subject) {
				return false
			}
		case "has":
			if len(mail.Attachments) == 0 {
				return false
			}
		case "is":
			if term.value == "unread" && !mail.Unread || term.value == "read" && mail.Unread || term.value == "flagged" && mail.FlagColor == "" {
				return false
			}
		case "after", "before":
			date, _ := time.Parse("2006-01-02", term.value)
			if term.key == "after" && mail.Date.Before(date) || term.key == "before" && !mail.Date.Before(date) {
				return false
			}
		}
	}
	return true
}
