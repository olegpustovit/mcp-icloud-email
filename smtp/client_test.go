package smtp

import (
	"context"
	"testing"
)

func TestSendEmailRejectsImpersonationAndUnsafeHeaders(t *testing.T) {
	c := NewClient("owner@icloud.com", "secret")
	for _, tc := range []struct {
		name    string
		from    string
		headers map[string]string
	}{
		{name: "impersonation", from: "victim@example.com"},
		{name: "header injection", from: "owner@icloud.com", headers: map[string]string{"References": "<a>\r\nBcc: victim@example.com"}},
		{name: "reserved header", from: "owner@icloud.com", headers: map[string]string{"From": "victim@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := c.SendEmail(context.Background(), tc.from, []string{"to@example.com"}, "subject", "body", SendOptions{Headers: tc.headers})
			if err == nil {
				t.Fatal("expected validation error before network send")
			}
		})
	}
}

func TestReplyAllExcludesOnlyAuthenticatedAddress(t *testing.T) {
	if !isAccountAddress("Owner <owner@icloud.com>", "owner@icloud.com") {
		t.Fatal("expected account address to match")
	}
	if isAccountAddress("owner@icloud.com.attacker.test", "owner@icloud.com") {
		t.Fatal("substring match must not treat another address as the account")
	}
}
