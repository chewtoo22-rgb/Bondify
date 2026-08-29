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
	MinClaimSecretBytes = 16
	MaxClaimSecretBytes = 64
	MaxAccountIDRunes   = 128
	MaxClaims           = 4096
	MinClaimTTL         = time.Minute
	MaxClaimTTL         = 24 * time.Hour
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

	now := s.now().UTC()
	s.pruneExpiredLocked(now)
	if len(s.claims) >= s.maxClaims {
		return EnrollmentClaim{}, ErrClaimCapacity
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
	accountID, err := normalizeAccountID(accountID)
	if err != nil || !validClaimID(claimID) || len(secret) < MinClaimSecretBytes || len(secret) > MaxClaimSecretBytes {
		return ErrClaimInvalid
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().UTC()
	s.pruneExpiredLocked(now)
	record, ok := s.claims[claimID]
	if !ok || record.accountID != accountID {
		return ErrClaimInvalid
	}

	providedHash := sha256.Sum256(secret)
	if subtle.ConstantTimeCompare(providedHash[:], record.secretHash[:]) != 1 {
		return ErrClaimInvalid
	}

	delete(s.claims, claimID)
	return nil
}

func (s *ClaimStore) pruneExpiredLocked(now time.Time) {
	for id, record := range s.claims {
		if !now.Before(record.expiresAt) {
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
