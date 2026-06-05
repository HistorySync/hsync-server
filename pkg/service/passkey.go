package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/auth"
	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/repository"
)

const (
	passkeyChallengeRegistration = "registration"
	passkeyChallengeLogin        = "login"
	passkeyChallengeTTL          = 2 * time.Minute
)

type PasskeyService struct {
	repos        *repository.Repos
	passkeys     passkeyStore
	tokenManager *auth.TokenManager
	settings     *SettingsService
	now          func() time.Time
}

type passkeyStore interface {
	CreateCredential(ctx context.Context, credential *model.PasskeyCredential) error
	ListCredentialsByUser(ctx context.Context, userID uuid.UUID) ([]model.PasskeyCredential, error)
	GetCredentialByIDForUser(ctx context.Context, userID, id uuid.UUID) (*model.PasskeyCredential, error)
	GetCredentialByCredentialID(ctx context.Context, credentialID []byte) (*model.PasskeyCredential, error)
	UpdateCredentialAfterUse(ctx context.Context, credential *model.PasskeyCredential, now time.Time) error
	DeleteCredentialByUser(ctx context.Context, userID, id uuid.UUID) (bool, error)
	SaveChallenge(ctx context.Context, challenge *model.PasskeyChallenge) error
	ConsumeChallenge(ctx context.Context, id uuid.UUID, challengeType string, userID *uuid.UUID, now time.Time) (*model.PasskeyChallenge, error)
}

type PasskeyBeginResult struct {
	ChallengeID uuid.UUID `json:"challenge_id"`
	Options     any       `json:"options"`
	ExpiresIn   int64     `json:"expires_in"`
}

type PasskeyFinishRegistrationInput struct {
	ChallengeID uuid.UUID
	Name        string
	Request     *http.Request
}

type PasskeyFinishLoginInput struct {
	ChallengeID uuid.UUID
	Request     *http.Request
}

type passkeyUser struct {
	user        *model.User
	credentials []model.PasskeyCredential
}

func NewPasskeyService(repos *repository.Repos, tokenManager *auth.TokenManager, settings *SettingsService) *PasskeyService {
	var passkeys passkeyStore
	if repos != nil {
		passkeys = repos.Passkeys
	}
	return &PasskeyService{
		repos:        repos,
		passkeys:     passkeys,
		tokenManager: tokenManager,
		settings:     settings,
		now:          time.Now,
	}
}

func (s *PasskeyService) ListCredentials(ctx context.Context, userID uuid.UUID) ([]model.PasskeyCredentialView, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	credentials, err := s.passkeys.ListCredentialsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	views := make([]model.PasskeyCredentialView, 0, len(credentials))
	for _, credential := range credentials {
		views = append(views, credential.View())
	}
	return views, nil
}

func (s *PasskeyService) DeleteCredential(ctx context.Context, userID, credentialID uuid.UUID) error {
	if err := s.ready(); err != nil {
		return err
	}
	deleted, err := s.passkeys.DeleteCredentialByUser(ctx, userID, credentialID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrPasskeyNotFound
	}
	return nil
}

func (s *PasskeyService) BeginRegistration(ctx context.Context, userID uuid.UUID, r *http.Request) (*PasskeyBeginResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !s.enabled(ctx) {
		return nil, ErrPasskeyDisabled
	}
	user, credentials, err := s.getActiveUserWithCredentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	wa, err := s.webAuthn(ctx, r)
	if err != nil {
		return nil, err
	}
	waUser := &passkeyUser{user: user, credentials: credentials}
	var options []webauthn.RegistrationOption
	if len(credentials) > 0 {
		exclusions := make([]protocol.CredentialDescriptor, 0, len(credentials))
		for _, credential := range credentials {
			exclusions = append(exclusions, credential.ToWebAuthnCredential().Descriptor())
		}
		options = append(options, webauthn.WithExclusions(exclusions))
	}
	creation, session, err := wa.BeginRegistration(waUser, options...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPasskeyVerification, err)
	}
	return s.saveSession(ctx, userID, passkeyChallengeRegistration, session, creation)
}

func (s *PasskeyService) FinishRegistration(ctx context.Context, userID uuid.UUID, input PasskeyFinishRegistrationInput) (*model.PasskeyCredentialView, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !s.enabled(ctx) {
		return nil, ErrPasskeyDisabled
	}
	user, credentials, err := s.getActiveUserWithCredentials(ctx, userID)
	if err != nil {
		return nil, err
	}
	session, err := s.consumeSession(ctx, input.ChallengeID, passkeyChallengeRegistration, &userID)
	if err != nil {
		return nil, err
	}
	wa, err := s.webAuthn(ctx, input.Request)
	if err != nil {
		return nil, err
	}
	credential, err := wa.FinishRegistration(&passkeyUser{user: user, credentials: credentials}, *session, input.Request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPasskeyVerification, err)
	}
	if existing, err := s.passkeys.GetCredentialByCredentialID(ctx, credential.ID); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("%w: credential already registered", ErrPasskeyVerification)
	}
	record := model.NewPasskeyCredentialFromWebAuthn(userID, strings.TrimSpace(input.Name), credential)
	if record.Name == "" {
		record.Name = "Passkey"
	}
	if err := s.passkeys.CreateCredential(ctx, record); err != nil {
		return nil, fmt.Errorf("create passkey credential: %w", err)
	}
	view := record.View()
	return &view, nil
}

func (s *PasskeyService) BeginLogin(ctx context.Context, r *http.Request) (*PasskeyBeginResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !s.enabled(ctx) {
		return nil, ErrPasskeyDisabled
	}
	wa, err := s.webAuthn(ctx, r)
	if err != nil {
		return nil, err
	}
	assertion, session, err := wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPasskeyVerification, err)
	}
	return s.saveSession(ctx, uuid.Nil, passkeyChallengeLogin, session, assertion)
}

func (s *PasskeyService) FinishLogin(ctx context.Context, input PasskeyFinishLoginInput) (*RegisterResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !s.enabled(ctx) {
		return nil, ErrPasskeyDisabled
	}
	session, err := s.consumeSession(ctx, input.ChallengeID, passkeyChallengeLogin, nil)
	if err != nil {
		return nil, err
	}
	wa, err := s.webAuthn(ctx, input.Request)
	if err != nil {
		return nil, err
	}
	var matched *model.PasskeyCredential
	waUser, validated, err := wa.FinishPasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		credential, err := s.passkeys.GetCredentialByCredentialID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		if credential == nil {
			return nil, ErrPasskeyNotFound
		}
		user, err := s.repos.Users.GetByID(ctx, credential.UserID)
		if err != nil {
			return nil, err
		}
		if user == nil || user.Status != model.StatusActive {
			return nil, ErrInvalidCredentials
		}
		if len(userHandle) > 0 {
			handleID, parseErr := uuid.Parse(string(userHandle))
			if parseErr != nil || handleID != user.ID {
				return nil, ErrPasskeyVerification
			}
		}
		matched = credential
		return &passkeyUser{user: user, credentials: []model.PasskeyCredential{*credential}}, nil
	}, *session, input.Request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPasskeyVerification, err)
	}
	userWrapper, ok := waUser.(*passkeyUser)
	if !ok || userWrapper.user == nil || matched == nil {
		return nil, ErrPasskeyVerification
	}
	if err := s.recordUse(ctx, matched, validated); err != nil {
		return nil, err
	}
	return (&AuthService{repos: s.repos, tokenManager: s.tokenManager}).issueTokens(ctx, userWrapper.user)
}

func (s *PasskeyService) ready() error {
	if s == nil || s.repos == nil || s.passkeys == nil || s.tokenManager == nil {
		return fmt.Errorf("passkey service is not configured")
	}
	return nil
}

func (s *PasskeyService) saveSession(ctx context.Context, userID uuid.UUID, challengeType string, session *webauthn.SessionData, options any) (*PasskeyBeginResult, error) {
	payload, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("marshal passkey session: %w", err)
	}
	var owner *uuid.UUID
	if userID != uuid.Nil {
		owner = &userID
	}
	expiresAt := s.now().Add(passkeyChallengeTTL)
	if !session.Expires.IsZero() && session.Expires.Before(expiresAt) {
		expiresAt = session.Expires
	}
	challenge := &model.PasskeyChallenge{
		UserID:      owner,
		Type:        challengeType,
		Challenge:   session.Challenge,
		SessionJSON: payload,
		ExpiresAt:   expiresAt,
	}
	if err := s.passkeys.SaveChallenge(ctx, challenge); err != nil {
		return nil, fmt.Errorf("save passkey challenge: %w", err)
	}
	return &PasskeyBeginResult{
		ChallengeID: challenge.ID,
		Options:     options,
		ExpiresIn:   int64(expiresAt.Sub(s.now()) / time.Second),
	}, nil
}

func (s *PasskeyService) consumeSession(ctx context.Context, id uuid.UUID, challengeType string, userID *uuid.UUID) (*webauthn.SessionData, error) {
	challenge, err := s.passkeys.ConsumeChallenge(ctx, id, challengeType, userID, s.now())
	if err != nil {
		return nil, err
	}
	if challenge == nil {
		return nil, ErrPasskeyChallenge
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(challenge.SessionJSON, &session); err != nil {
		return nil, fmt.Errorf("unmarshal passkey session: %w", err)
	}
	return &session, nil
}

func (s *PasskeyService) recordUse(ctx context.Context, stored *model.PasskeyCredential, validated *webauthn.Credential) error {
	stored.ApplyValidatedCredential(validated)
	return s.passkeys.UpdateCredentialAfterUse(ctx, stored, s.now())
}

func (s *PasskeyService) getActiveUserWithCredentials(ctx context.Context, userID uuid.UUID) (*model.User, []model.PasskeyCredential, error) {
	user, err := s.repos.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("get user: %w", err)
	}
	if user == nil || user.Status != model.StatusActive {
		return nil, nil, ErrUserNotFound
	}
	credentials, err := s.passkeys.ListCredentialsByUser(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	return user, credentials, nil
}

func (s *PasskeyService) enabled(ctx context.Context) bool {
	return s.settings != nil && s.settings.GetBoolOrDefault(ctx, SettingKeyPasskeyEnabled)
}

func (s *PasskeyService) webAuthn(ctx context.Context, r *http.Request) (*webauthn.WebAuthn, error) {
	settings, err := s.passkeySettings(ctx, r)
	if err != nil {
		return nil, err
	}
	return webauthn.New(&webauthn.Config{
		RPID:          settings.RPID,
		RPDisplayName: settings.RPName,
		RPOrigins:     settings.Origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyChallengeTTL, TimeoutUVD: passkeyChallengeTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyChallengeTTL, TimeoutUVD: passkeyChallengeTTL},
		},
	})
}

type resolvedPasskeySettings struct {
	RPID    string
	RPName  string
	Origins []string
}

func (s *PasskeyService) passkeySettings(ctx context.Context, r *http.Request) (resolvedPasskeySettings, error) {
	rpName, _ := s.settings.GetString(ctx, SettingKeyPasskeyRPName)
	rpName = strings.TrimSpace(rpName)
	if rpName == "" {
		rpName = "HistorySync"
	}
	originsRaw, _ := s.settings.GetString(ctx, SettingKeyPasskeyOrigins)
	origins, err := resolvePasskeyOrigins(r, originsRaw)
	if err != nil {
		return resolvedPasskeySettings{}, err
	}
	rpID, _ := s.settings.GetString(ctx, SettingKeyPasskeyRPID)
	rpID, err = resolvePasskeyRPID(rpID, origins)
	if err != nil {
		return resolvedPasskeySettings{}, err
	}
	return resolvedPasskeySettings{RPID: rpID, RPName: rpName, Origins: origins}, nil
}

func resolvePasskeyOrigins(r *http.Request, configured string) ([]string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		parts := strings.Split(configured, ",")
		origins := make([]string, 0, len(parts))
		for _, part := range parts {
			origin := strings.TrimSpace(part)
			if origin == "" {
				continue
			}
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" {
				return nil, fmt.Errorf("%w: invalid passkey origin", ErrPasskeyVerification)
			}
			origins = append(origins, origin)
		}
		if len(origins) == 0 {
			return nil, fmt.Errorf("%w: passkey origins are empty", ErrPasskeyVerification)
		}
		return origins, nil
	}
	if r == nil || r.Host == "" {
		return nil, fmt.Errorf("%w: passkey origin is not configured", ErrPasskeyVerification)
	}
	scheme := requestScheme(r)
	host := r.Host
	if !isLocalhost(host) {
		return nil, fmt.Errorf("%w: passkey origins must be configured for non-localhost hosts", ErrPasskeyVerification)
	}
	if scheme != "https" && scheme != "http" {
		return nil, fmt.Errorf("%w: invalid passkey origin scheme", ErrPasskeyVerification)
	}
	return []string{scheme + "://" + host}, nil
}

func resolvePasskeyRPID(configured string, origins []string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		rpID := hostWithoutPort(configured)
		if !rpIDAllowedForOrigins(rpID, origins) {
			return "", fmt.Errorf("%w: passkey rp id does not match configured origins", ErrPasskeyVerification)
		}
		return rpID, nil
	}
	if len(origins) == 0 {
		return "", fmt.Errorf("%w: passkey origin is not configured", ErrPasskeyVerification)
	}
	parsed, err := url.Parse(origins[0])
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%w: invalid passkey origin", ErrPasskeyVerification)
	}
	return hostWithoutPort(parsed.Host), nil
}

func rpIDAllowedForOrigins(rpID string, origins []string) bool {
	rpID = strings.ToLower(strings.TrimSpace(rpID))
	if rpID == "" {
		return false
	}
	for _, origin := range origins {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" {
			return false
		}
		host := strings.ToLower(hostWithoutPort(parsed.Host))
		if host == rpID || strings.HasSuffix(host, "."+rpID) {
			continue
		}
		return false
	}
	return true
}

func requestScheme(r *http.Request) string {
	if r == nil {
		return "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return strings.ToLower(strings.TrimSpace(strings.Split(proto, ",")[0]))
	}
	if r.TLS != nil {
		return "https"
	}
	if r.URL != nil && r.URL.Scheme != "" {
		return strings.ToLower(r.URL.Scheme)
	}
	return "http"
}

func hostWithoutPort(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func isLocalhost(host string) bool {
	host = hostWithoutPort(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (u *passkeyUser) WebAuthnID() []byte {
	if u == nil || u.user == nil {
		return nil
	}
	return []byte(u.user.ID.String())
}

func (u *passkeyUser) WebAuthnName() string {
	if u == nil || u.user == nil {
		return ""
	}
	return u.user.Email
}

func (u *passkeyUser) WebAuthnDisplayName() string {
	if u == nil || u.user == nil {
		return ""
	}
	if strings.TrimSpace(u.user.DisplayName) != "" {
		return u.user.DisplayName
	}
	return u.user.Email
}

func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	if u == nil {
		return nil
	}
	out := make([]webauthn.Credential, 0, len(u.credentials))
	for _, credential := range u.credentials {
		out = append(out, credential.ToWebAuthnCredential())
	}
	return out
}
