package enrollment

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestClaimStoreRevokeIsAccountScopedAndImmediate(t *testing.T) {
	now := time.Date(2026, 9, 1, 13, 30, 0, 0, time.UTC)
	claims, err := NewClaimStore(8, 10*time.Minute, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	claim, secret, err := claims.IssueGenerated("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := claims.Revoke("acct-b", claim.ID); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("cross-account revoke error = %v, want %v", err, ErrClaimInvalid)
	}
	if err := claims.Consume("acct-a", claim.ID, secret); err != nil {
		t.Fatalf("cross-account revoke damaged valid claim: %v", err)
	}

	claim, secret, err = claims.IssueGenerated("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := claims.Revoke("acct-a", claim.ID); err != nil {
		t.Fatal(err)
	}
	if err := claims.Consume("acct-a", claim.ID, secret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("consume after revoke error = %v, want %v", err, ErrClaimInvalid)
	}
	if err := claims.Revoke("acct-a", claim.ID); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("repeat revoke error = %v, want %v", err, ErrClaimInvalid)
	}
}

func TestClaimRevokeWireStrictAndBounded(t *testing.T) {
	claimID := strings.Repeat("a", 32)
	payload := []byte(fmt.Sprintf("{\"claim_id\":%q}", claimID))
	got, err := DecodeClaimRevokeWire(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != claimID {
		t.Fatalf("claim id = %q, want %q", got, claimID)
	}

	badPayloads := [][]byte{
		nil,
		[]byte("{\"claim_id\":\"bad\"}"),
		[]byte(fmt.Sprintf("{\"claim_id\":%q,\"account_id\":\"acct-a\"}", claimID)),
		[]byte(fmt.Sprintf("{\"claim_id\":%q}{}", claimID)),
		bytes.Repeat([]byte("x"), MaxClaimRevokeWireBytes+1),
	}
	for _, bad := range badPayloads {
		if _, err := DecodeClaimRevokeWire(bad); !errors.Is(err, ErrClaimManagementWire) {
			t.Fatalf("DecodeClaimRevokeWire(%q) error = %v", bad, err)
		}
	}

	result, err := EncodeClaimRevokeResult(claimID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) > MaxClaimRevokeResultBytes {
		t.Fatalf("result size = %d, max %d", len(result), MaxClaimRevokeResultBytes)
	}
	want := fmt.Sprintf("{\"version\":1,\"claim_id\":%q,\"revoked\":true}", claimID)
	if string(result) != want {
		t.Fatalf("result = %s, want %s", result, want)
	}
	if _, err := EncodeClaimRevokeResult("bad"); !errors.Is(err, ErrClaimManagementWire) {
		t.Fatalf("invalid result id error = %v", err)
	}
}

func TestAccountServiceRevokeEnrollmentClaimResultWire(t *testing.T) {
	now := time.Date(2026, 9, 1, 13, 30, 0, 0, time.UTC)
	claims, err := NewClaimStore(8, 10*time.Minute, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewDeviceRegistry(4)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAccountService(claims, registry, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	claim, secret, err := service.IssueEnrollmentClaim("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf("{\"claim_id\":%q}", claim.ID))

	result, err := service.RevokeEnrollmentClaimResultWire("acct-b", payload)
	if !errors.Is(err, ErrClaimInvalid) || result != nil {
		t.Fatalf("cross-account result = %q, error = %v", result, err)
	}

	result, err = service.RevokeEnrollmentClaimResultWire("acct-a", payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(result, []byte("\"revoked\":true")) || !bytes.Contains(result, []byte(claim.ID)) {
		t.Fatalf("unexpected revoke result: %s", result)
	}
	if bytes.Contains(result, []byte("acct-a")) || bytes.Contains(result, secret) {
		t.Fatalf("revoke result leaked account identity or claim secret: %s", result)
	}
	if err := claims.Consume("acct-a", claim.ID, secret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("revoked claim remained usable: %v", err)
	}

	var nilService *AccountService
	result, err = nilService.RevokeEnrollmentClaimResultWire("acct-a", payload)
	if !errors.Is(err, ErrEnrollmentStore) || result != nil {
		t.Fatalf("nil service result = %q, error = %v", result, err)
	}
}
