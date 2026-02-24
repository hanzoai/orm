package db

import (
	"sync"
)

// Manager is the main entry point for database operations.
// It manages multiple database layers and provides unified access.
type Manager struct {
	config *Config
	mu     sync.RWMutex

	// User databases (userID -> DB)
	userDBs map[string]DB

	// Organization databases (orgID -> DB)
	orgDBs map[string]DB

	// Analytics store (shared)
	analyticsStore AnalyticsStore

	closed bool
}

// NewManager creates a new database manager.
func NewManager(cfg *Config) (*Manager, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	if cfg.UserDataDir == "" {
		cfg.UserDataDir = cfg.DataDir + "/users"
	}
	if cfg.OrgDataDir == "" {
		cfg.OrgDataDir = cfg.DataDir + "/orgs"
	}

	m := &Manager{
		config:  cfg,
		userDBs: make(map[string]DB),
		orgDBs:  make(map[string]DB),
	}

	return m, nil
}

// SetAnalyticsStore sets the analytics store for the manager.
func (m *Manager) SetAnalyticsStore(store AnalyticsStore) {
	m.analyticsStore = store
}

// Analytics returns the analytics store.
func (m *Manager) Analytics() AnalyticsStore {
	return m.analyticsStore
}

// RegisterUserDB registers a user database.
func (m *Manager) RegisterUserDB(userID string, db DB) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.userDBs[userID] = db
}

// RegisterOrgDB registers an organization database.
func (m *Manager) RegisterOrgDB(orgID string, db DB) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orgDBs[orgID] = db
}

// User returns the database for a specific user.
func (m *Manager) User(userID string) (DB, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrDatabaseClosed
	}

	if db, ok := m.userDBs[userID]; ok {
		return db, nil
	}

	return nil, ErrNoSuchEntity
}

// Org returns the database for a specific organization.
func (m *Manager) Org(orgID string) (DB, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.closed {
		return nil, ErrDatabaseClosed
	}

	if db, ok := m.orgDBs[orgID]; ok {
		return db, nil
	}

	return nil, ErrNoSuchEntity
}

// Close closes all database connections.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	var lastErr error

	for _, db := range m.userDBs {
		if err := db.Close(); err != nil {
			lastErr = err
		}
	}

	for _, db := range m.orgDBs {
		if err := db.Close(); err != nil {
			lastErr = err
		}
	}

	if m.analyticsStore != nil {
		if err := m.analyticsStore.Close(); err != nil {
			lastErr = err
		}
	}

	return lastErr
}
