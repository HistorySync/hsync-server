// Package provider contains the default (CE) AuthProvider implementation.
package provider

// SingleUserAuthProvider is the CE default: supports only one pre-configured user.
type SingleUserAuthProvider struct {
	adminUser *UserInfo
}

// NewSingleUserAuthProvider creates a SingleUserAuthProvider for the given user.
func NewSingleUserAuthProvider(user *UserInfo) *SingleUserAuthProvider {
	return &SingleUserAuthProvider{adminUser: user}
}

// ValidateCredentials checks the provided email against the single configured user.
func (p *SingleUserAuthProvider) ValidateCredentials(email, password string) (*UserInfo, error) {
	// CE verifies only that the email matches; password is checked by
	// the caller (e.g., API key comparison or config password).
	if email != p.adminUser.Email {
		return nil, ErrInvalidCredentials
	}
	return p.adminUser, nil
}

// CreateUser always returns an error in CE mode.
func (p *SingleUserAuthProvider) CreateUser(req CreateUserRequest) (*UserInfo, error) {
	return nil, ErrMultiUserNotSupported
}

// GetUserByID returns the single user if IDs match.
func (p *SingleUserAuthProvider) GetUserByID(userID string) (*UserInfo, error) {
	if userID == p.adminUser.ID {
		return p.adminUser, nil
	}
	return nil, ErrInvalidCredentials
}

// SupportsMultiUser always returns false for CE.
func (p *SingleUserAuthProvider) SupportsMultiUser() bool {
	return false
}

// defaultAuthProvider is a lazy-init singleton used before configuration is loaded.
// It's replaced with a properly configured SingleUserAuthProvider after config load.
var defaultAuthProvider AuthProvider = &noopAuthProvider{}

type noopAuthProvider struct{}

func (p *noopAuthProvider) ValidateCredentials(email, password string) (*UserInfo, error) {
	return nil, ErrInvalidCredentials
}
func (p *noopAuthProvider) CreateUser(req CreateUserRequest) (*UserInfo, error) {
	return nil, ErrMultiUserNotSupported
}
func (p *noopAuthProvider) GetUserByID(userID string) (*UserInfo, error) {
	return nil, ErrInvalidCredentials
}
func (p *noopAuthProvider) SupportsMultiUser() bool {
	return false
}
