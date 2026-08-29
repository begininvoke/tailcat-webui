package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ca-x/tailcat-webui/ent"
	"github.com/ca-x/tailcat-webui/ent/loginflow"
	"github.com/ca-x/tailcat-webui/ent/session"
	"github.com/ca-x/tailcat-webui/ent/user"
	"github.com/ca-x/tailcat-webui/internal/config"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrOIDCDisabled = errors.New("OIDC is not configured")
)

type Principal struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

type Service struct {
	db          *ent.Client
	provider    *oidc.Provider
	verifier    *oidc.IDTokenVerifier
	oauth       oauth2.Config
	cfg         config.Config
	logger      *slog.Logger
	flowTTL     time.Duration
	now         func() time.Time
	lastCleanup atomic.Int64
}

func NewService(ctx context.Context, db *ent.Client, cfg config.Config, logger *slog.Logger) (*Service, error) {
	if db == nil {
		return nil, errors.New("auth service: nil database")
	}
	s := &Service{db: db, cfg: cfg, logger: logger, flowTTL: 10 * time.Minute, now: time.Now}
	if !cfg.OIDC.Enabled() {
		return s, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	s.provider = provider
	s.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID})
	s.oauth = oauth2.Config{
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: cfg.OIDC.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.RedirectURL(),
		Scopes:       cfg.OIDC.Scopes,
	}
	return s, nil
}

func (s *Service) StartLogin(ctx context.Context, returnTo string) (string, error) {
	if s.provider == nil {
		return "", ErrOIDCDisabled
	}
	s.cleanupExpired(ctx)
	state, err := randomToken(32)
	if err != nil {
		return "", err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	if !safeReturnTo(returnTo) {
		returnTo = "/"
	}
	_, err = s.db.LoginFlow.Create().
		SetStateHash(tokenHash(state)).
		SetNonce(nonce).
		SetCodeVerifier(verifier).
		SetReturnTo(returnTo).
		SetExpiresAt(s.now().Add(s.flowTTL)).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("store OIDC login flow: %w", err)
	}
	return s.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

func (s *Service) CompleteLogin(ctx context.Context, state, code string) (Principal, string, string, error) {
	if s.provider == nil {
		return Principal{}, "", "", ErrOIDCDisabled
	}
	flow, err := s.db.LoginFlow.Query().Where(loginflow.StateHashEQ(tokenHash(state))).Only(ctx)
	if err != nil {
		return Principal{}, "", "", ErrUnauthorized
	}
	if err := s.db.LoginFlow.DeleteOneID(flow.ID).Exec(ctx); err != nil {
		return Principal{}, "", "", fmt.Errorf("consume OIDC login flow: %w", err)
	}
	if !flow.ExpiresAt.After(s.now()) || code == "" {
		return Principal{}, "", "", ErrUnauthorized
	}
	token, err := s.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow.CodeVerifier))
	if err != nil {
		return Principal{}, "", "", ErrUnauthorized
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Principal{}, "", "", ErrUnauthorized
	}
	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil || idToken.Nonce != flow.Nonce || idToken.Subject == "" {
		return Principal{}, "", "", ErrUnauthorized
	}
	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Principal{}, "", "", ErrUnauthorized
	}
	displayName := claims.Name
	if displayName == "" {
		displayName = claims.PreferredUsername
	}
	principal, err := s.upsertUser(ctx, idToken.Issuer, idToken.Subject, claims.Email, displayName, claims.Picture)
	if err != nil {
		return Principal{}, "", "", err
	}
	sessionToken, err := s.createSession(ctx, principal.ID)
	if err != nil {
		return Principal{}, "", "", err
	}
	return principal, sessionToken, flow.ReturnTo, nil
}

func (s *Service) DemoLogin(ctx context.Context) (Principal, string, error) {
	if !s.cfg.DemoMode {
		return Principal{}, "", ErrUnauthorized
	}
	principal, err := s.upsertUser(ctx, "demo://local", "operator", s.cfg.DemoEmail, "Tailcat Operator", "")
	if err != nil {
		return Principal{}, "", err
	}
	token, err := s.createSession(ctx, principal.ID)
	return principal, token, err
}

func (s *Service) ResolveSession(ctx context.Context, rawToken string) (Principal, error) {
	if rawToken == "" {
		return Principal{}, ErrUnauthorized
	}
	record, err := s.db.Session.Query().Where(session.TokenHashEQ(tokenHash(rawToken))).WithUser().Only(ctx)
	if err != nil || record.Edges.User == nil {
		return Principal{}, ErrUnauthorized
	}
	now := s.now()
	if !record.ExpiresAt.After(now) || record.LastSeenAt.Add(s.cfg.SessionIdle).Before(now) {
		_ = s.db.Session.DeleteOneID(record.ID).Exec(ctx)
		return Principal{}, ErrUnauthorized
	}
	if now.Sub(record.LastSeenAt) > 5*time.Minute {
		if err := record.Update().SetLastSeenAt(now).Exec(ctx); err != nil {
			s.logger.WarnContext(ctx, "Update session activity failed", "error", err)
		}
	}
	return principalOf(record.Edges.User), nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	_, err := s.db.Session.Delete().Where(session.TokenHashEQ(tokenHash(rawToken))).Exec(ctx)
	return err
}

func (s *Service) createSession(ctx context.Context, userID string) (string, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", err
	}
	now := s.now()
	s.cleanupExpired(ctx)
	_, err = s.db.Session.Create().
		SetUserID(userID).
		SetTokenHash(tokenHash(raw)).
		SetExpiresAt(now.Add(s.cfg.SessionMax)).
		SetLastSeenAt(now).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return raw, nil
}

func (s *Service) cleanupExpired(ctx context.Context) {
	now := s.now()
	previous := s.lastCleanup.Load()
	if previous != 0 && now.Sub(time.Unix(previous, 0)) < time.Hour {
		return
	}
	if !s.lastCleanup.CompareAndSwap(previous, now.Unix()) {
		return
	}
	_, _ = s.db.LoginFlow.Delete().Where(loginflow.ExpiresAtLT(now)).Exec(ctx)
	_, _ = s.db.Session.Delete().Where(session.ExpiresAtLT(now)).Exec(ctx)
}

func (s *Service) upsertUser(ctx context.Context, issuer, subject, email, displayName, avatar string) (Principal, error) {
	issuer, subject = truncate(issuer, 2048), truncate(subject, 512)
	email, displayName, avatar = truncate(email, 320), truncate(displayName, 256), truncate(avatar, 2048)
	record, err := s.db.User.Query().Where(user.IssuerEQ(issuer), user.SubjectEQ(subject)).Only(ctx)
	if ent.IsNotFound(err) {
		record, err = s.db.User.Create().
			SetIssuer(issuer).
			SetSubject(subject).
			SetEmail(email).
			SetDisplayName(displayName).
			SetAvatarURL(avatar).
			Save(ctx)
		if ent.IsConstraintError(err) {
			record, err = s.db.User.Query().Where(user.IssuerEQ(issuer), user.SubjectEQ(subject)).Only(ctx)
			if err == nil {
				record, err = record.Update().SetEmail(email).SetDisplayName(displayName).SetAvatarURL(avatar).Save(ctx)
			}
		}
	} else if err == nil {
		record, err = record.Update().SetEmail(email).SetDisplayName(displayName).SetAvatarURL(avatar).Save(ctx)
	}
	if err != nil {
		return Principal{}, fmt.Errorf("upsert OIDC user: %w", err)
	}
	return principalOf(record), nil
}

func truncate(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func principalOf(record *ent.User) Principal {
	return Principal{ID: record.ID, Email: record.Email, DisplayName: record.DisplayName, AvatarURL: record.AvatarURL}
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func safeReturnTo(raw string) bool {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\r\n\\") {
		return false
	}
	target, err := url.Parse(raw)
	return err == nil && target.Scheme == "" && target.Host == "" && strings.HasPrefix(target.Path, "/") && !strings.HasPrefix(target.Path, "//") && !strings.Contains(target.Path, "\\") && target.Path != "/r" && !strings.HasPrefix(target.Path, "/r/")
}
