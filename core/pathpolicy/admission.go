package pathpolicy

import (
    "errors"
    "sort"
    "strings"
)

const MaxObservedInterfaces = 32

var (
    ErrNoEligibleInterfaces = errors.New("no eligible interfaces")
    ErrTooManyInterfaces     = errors.New("too many observed interfaces")
    ErrDuplicateInterface   = errors.New("duplicate interface id")
    ErrInvalidInterfaceID   = errors.New("invalid interface id")
)

// ObservedInterface is the platform-neutral state required to decide whether
// an uplink may participate in a Bondify session.
type ObservedInterface struct {
    ID        string
    Up        bool
    HasRoute  bool
    Metered   bool
}

// Policy is deliberately small and fail-closed. ExplicitIDs, when non-empty,
// acts as an allowlist. AllowMetered controls whether metered links may enter
// the candidate set.
type Policy struct {
    ExplicitIDs []string
    AllowMetered bool
}

// Admit validates observed interface identity and returns a deterministic list
// of currently eligible interface IDs. It never mutates platform networking.
func Admit(observed []ObservedInterface, policy Policy) ([]string, error) {
    if len(observed) > MaxObservedInterfaces {
        return nil, ErrTooManyInterfaces
    }

    allow := map[string]struct{}{}
    for _, raw := range policy.ExplicitIDs {
        id, err := normalizeID(raw)
        if err != nil {
            return nil, err
        }
        if _, exists := allow[id]; exists {
            return nil, ErrDuplicateInterface
        }
        allow[id] = struct{}{}
    }

    seen := map[string]struct{}{}
    eligible := make([]string, 0, len(observed))
    for _, item := range observed {
        id, err := normalizeID(item.ID)
        if err != nil {
            return nil, err
        }
        if _, exists := seen[id]; exists {
            return nil, ErrDuplicateInterface
        }
        seen[id] = struct{}{}

        if !item.Up || !item.HasRoute {
            continue
        }
        if item.Metered && !policy.AllowMetered {
            continue
        }
        if len(allow) > 0 {
            if _, ok := allow[id]; !ok {
                continue
            }
        }
        eligible = append(eligible, id)
    }

    if len(eligible) == 0 {
        return nil, ErrNoEligibleInterfaces
    }
    sort.Strings(eligible)
    return eligible, nil
}

func normalizeID(raw string) (string, error) {
    id := strings.TrimSpace(raw)
    if id == "" || len(id) > 64 {
        return "", ErrInvalidInterfaceID
    }
    for _, r := range id {
        if r < 0x20 || r == 0x7f {
            return "", ErrInvalidInterfaceID
        }
    }
    return id, nil
}
