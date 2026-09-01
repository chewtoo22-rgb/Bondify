package enrollment

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	MinClaimSecretBytes       = 16
	MaxClaimSecretBytes       = 64
	GeneratedClaimSecretBytes = 32
	MaxAccountIDRunes         = 128
	MaxClaims                 = 4096
	MinClaimTTL               = time.Minute
	MaxClaimTTL               = 24 * time.Hour
)

var (
	ErrClaimConfig   = errors.New("invalid enrollment claim configuration")
	ErrClaimSecret   = errors.New("invalid enrollment claim secret")
	ErrAccountID     = errors.New("invalid account id")
	ErrClaimCapacity = errors.New("enrollment claim capacity reached")
	ErrClaimInvalid  = errors.New("invalid or expired enrollment claim")
	ErrClaimRandom   = errors.New("unable to generate enrollment claim id")
)

type EnrollmentClaim struct {
	ID        string
	ExpiresAt time.Time
}

type claimRecord struct {
	accountID  string
	secretHash [32]byte
	expiresAt  time.Time
	reserved   bool
}

// ClaimStore is an in-memory, bounded, concurrency-safe registry for short-lived
// one-time enrollment claims. It deliberately stores only a hash of the claim
// secret and deletes a claim after the first successful consume.
type ClaimStore struct {
	mu        sync.Mutex
	claims    map[string]claimRecord
	maxClaims int
	ttl       time.Duration
	random    io.Reader
	now       func() time.Time
}

func NewClaimStore(maxClaims int, ttl time.Duration, random io.Reader, now func() time.Time) (*ClaimStore, error) {
	if maxClaims < 1 || maxClaims > MaxClaims || ttl < MinClaimTTL || ttl > MaxClaimTTL {
		return nil, ErrClaimConfig
	}
	if random == nil {
		random = rand.Reader
	}
	if now == nil {
		now = time.Now
	}
	return &ClaimStore{
		claims:    make(map[string]claimRecord),
		maxClaims: maxClaims,
		ttl:       ttl,
		random:    random,
		now:       now,
	}, nil
}

func (s *ClaimStore) Issue(accountID string, secret []byte) (EnrollmentClaim, error) {
	accountID, err := normalizeAccountID(accountID)
	if err != nil {
		return EnrollmentClaim{}, err
	}
	if len(secret) < MinClaimSecretBytes || len(secret) > MaxClaimSecretBytes {
		return EnrollmentClaim{}, ErrClaimSecret
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issueLocked(accountID, secret)
}

// IssueGenerated creates a one-time enrollment claim with a cryptographically
// random secret suitable for returning once to an authenticated account client.
// Only the secret hash is retained by ClaimStore. The caller must treat the
// returned secret as a credential and avoid persisting it in logs or inventory.
func (s *ClaimStore) IssueGenerated(accountID string) (EnrollmentClaim, []byte, error) {
	accountID, err := normalizeAccountID(accountID)
	if err != nil {
		return EnrollmentClaim{}, nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	s.pruneExpiredLocked(now)
	if len(s.claims) >= s.maxClaims {
		return EnrollmentClaim{}, nil, ErrClaimCapacity
	}

	secret := make([]byte, GeneratedClaimSecretBytes)
	if _, err := io.ReadFull(s.random, secret); err != nil {
		return EnrollmentClaim{}, nil, ErrClaimRandom
	}
	claim, err := s.issueLockedAt(accountID, secret, now, false)
	if err != nil {
		return EnrollmentClaim{}, nil, err
	}
	return claim, secret, nil
}

func (s *ClaimStore) issueLocked(accountID string, secret []byte) (EnrollmentClaim, error) {
	now := s.now().UTC()
	return s.issueLockedAt(accountID, secret, now, true)
}

func (s *ClaimStore) issueLockedAt(accountID string, secret []byte, now time.Time, pruneAndCheckCapacity bool) (EnrollmentClaim, error) {
	if pruneAndCheckCapacity {
		s.pruneExpiredLocked(now)
		if len(s.claims) >= s.maxClaims {
			return EnrollmentClaim{}, ErrClaimCapacity
		}
	}

	for attempts := 0; attempts < 4; attempts++ {
		var rawID [16]byte
		if _, err := io.ReadFull(s.random, rawID[:]); err != nil {
			return EnrollmentClaim{}, ErrClaimRandom
		}
		id := hex.EncodeToString(rawID[:])
		if _, exists := s.claims[id]; exists {
			continue
		}
		expiresAt := now.Add(s.ttl)
		s.claims[id] = claimRecord{
			accountID:  accountID,
			secretHash: sha256.Sum256(secret),
			expiresAt:  expiresAt,
		}
		return EnrollmentClaim{ID: id, ExpiresAt: expiresAt}, nil
	}

	return EnrollmentClaim{}, ErrClaimRandom
}

func (s *ClaimStore) Consume(accountID, claimID string, secret []byte) error {
	return s.ConsumeAndCommit(accountID, claimID, secret, nil)
}

// Revoke invalidates an outstanding claim for the separately authenticated
// account. It intentionally does not require the claim secret: possession of a
// leaked enrollment code must not prevent the account owner from cancelling it.
// Cross-account, reserved, and already-consumed/expired claims fail closed with
// the same error so callers cannot use revocation as a claim-existence oracle.
func (s *ClaimStore) Revoke(accountID, claimID string) error {
	accountID, err := normalizeAccountID(accountID)
	if err != nil || !validClaimID(claimID) {
		return ErrClaimInvalid
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	s.pruneExpiredLocked(now)
	record, ok := s.claims[claimID]
	if !ok || record.accountID != accountID || record.reserved {
		return ErrClaimInvalid
	}
	delete(s.claims, claimID)
	return nil
}

// ConsumeAndCommit validates exclusive ownership of a one-time claim, reserves
// only that claim, then executes commit without holding the store-wide mutex.
// This keeps unrelated claim issuance/revocation/consumption responsive while a
// bounded local durable write is in progress. A failed commit releases the
// reservation when the claim is still valid; a successful commit burns it.
func (s *ClaimStore) ConsumeAndCommit(accountID, claimID string, secret []byte, commit func() error) error {
	accountID, err := normalizeAccountID(accountID)
	if err != nil || !validClaimID(claimID) || len(secret) < MinClaimSecretBytes || len(secret) > MaxClaimSecretBytes {
		return ErrClaimInvalid
	}

	providedHash := sha256.Sum256(secret)

	s.mu.Lock()
	now := s.now().UTC()
	s.pruneExpiredLocked(now)
	record, ok := s.claims[claimID]
	if !ok || record.accountID != accountID || record.reserved || subtle.ConstantTimeCompare(providedHash[:], record.secretHash[:]) != 1 {
		s.mu.Unlock()
		return ErrClaimInvalid
	}
	record.reserved = true
	s.claims[claimID] = record
	s.mu.Unlock()

	var commitErr error
	if commit != nil {
		commitErr = commit()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok = s.claims[claimID]
	if !ok || !record.reserved {
		// Reserved records are never pruned or revoked, so this is an internal
		// fail-closed guard against future mutation paths violating that rule.
		return ErrClaimInvalid
	}
	if commitErr == nil {
		delete(s.claims, claimID)
		return nil
	}

	now = s.now().UTC()
	if !now.Before(record.expiresAt) {
		delete(s.claims, claimID)
	} else {
		record.reserved = false
		s.claims[claimID] = record
	}
	return commitErr
}

func (s *ClaimStore) pruneExpiredLocked(now time.Time) {
	for id, record := range s.claims {
		if !record.reserved && !now.Before(record.expiresAt) {
			delete(s.claims, id)
		}
	}
}

func normalizeAccountID(accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || len([]rune(accountID)) > MaxAccountIDRunes {
		return "", ErrAccountID
	}
	for _, r := range accountID {
		if unicode.IsControl(r) {
			return "", ErrAccountID
		}
	}
	return accountID, nil
}

func validClaimID(id string) bool {
	if len(id) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16
}
