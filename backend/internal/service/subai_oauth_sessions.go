package service

import (
	"net/http"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// SubAIOAuthSession exposes only safe session metadata through the original API.
type SubAIOAuthSession struct {
	ProxyID   *int64 `json:"-"`
	ID        string `json:"id"`
	Status    string `json:"status"`
	AccountID int64  `json:"account_id,omitempty"`
}

func (s *OpenAIOAuthService) BindSubAISession(id string, ownerID, accountID int64) error {
	session, ok := s.sessionStore.Get(id)
	if !ok || session.OwnerID != ownerID {
		return infraerrors.New(http.StatusNotFound, "OPENAI_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	}
	session.AccountID = accountID
	if err := s.sessionStore.Set(id, session); err != nil {
		return err
	}
	// Alias stores only the canonical ID; both callback modes consume the same session.
	if err := s.sessionStore.Set("state:"+session.State, &openai.OAuthSession{State: id, CreatedAt: session.CreatedAt}); err != nil {
		return err
	}
	return s.RecordSubAIStatus(id, ownerID, accountID, "pending")
}

func (s *OpenAIOAuthService) RecordSubAIStatus(id string, ownerID, accountID int64, status string) error {
	return s.sessionStore.Set("status:"+id, &openai.OAuthSession{OwnerID: ownerID, AccountID: accountID, Status: status, CreatedAt: time.Now()})
}

func (s *OpenAIOAuthService) GetSubAISession(id string, ownerID int64) (*SubAIOAuthSession, error) {
	session, ok := s.sessionStore.Get("status:" + id)
	if !ok || session.OwnerID != ownerID {
		return nil, infraerrors.New(http.StatusNotFound, "OPENAI_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	}
	if session.Status == "pending" {
		if _, pending := s.sessionStore.Get(id); !pending {
			session.Status = "expired"
		}
	}
	result := &SubAIOAuthSession{ID: id, Status: session.Status, AccountID: session.AccountID}
	if pending, ok := s.sessionStore.Get(id); ok {
		result.ProxyID = pending.ProxyID
	}
	return result, nil
}

func (s *OpenAIOAuthService) ResolveSubAICallback(state string) (string, int64, string, error) {
	alias, ok := s.sessionStore.Get("state:" + state)
	if !ok {
		return "", 0, "", infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_INVALID_STATE", "invalid or expired authorization state")
	}
	session, ok := s.sessionStore.Get(alias.State)
	if !ok || session.State != state {
		return "", 0, "", infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	}
	return alias.State, session.OwnerID, session.RedirectURI, nil
}
