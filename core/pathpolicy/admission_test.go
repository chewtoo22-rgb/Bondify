package pathpolicy

import (
    "errors"
    "testing"
)

func TestAdmitDeterministicEligibleInterfaces(t *testing.T) {
    got, err := Admit([]ObservedInterface{
        {ID: "cellular", Up: true, HasRoute: true},
        {ID: "wifi", Up: true, HasRoute: true},
    }, Policy{AllowMetered: true})
    if err != nil { t.Fatalf("Admit() error = %v", err) }
    if len(got) != 2 || got[0] != "cellular" || got[1] != "wifi" { t.Fatalf("unexpected deterministic order: %#v", got) }
}

func TestAdmitRequiresUpAndRoute(t *testing.T) {
    got, err := Admit([]ObservedInterface{{ID:"wifi",Up:true,HasRoute:true},{ID:"cellular",Up:false,HasRoute:true},{ID:"ethernet",Up:true,HasRoute:false}}, Policy{AllowMetered:true})
    if err != nil { t.Fatalf("Admit() error = %v", err) }
    if len(got)!=1 || got[0]!="wifi" { t.Fatalf("unexpected candidates: %#v", got) }
}

func TestAdmitMeteredFailClosed(t *testing.T) {
    _, err := Admit([]ObservedInterface{{ID:"cellular",Up:true,HasRoute:true,Metered:true}}, Policy{})
    if !errors.Is(err, ErrNoEligibleInterfaces) { t.Fatalf("expected ErrNoEligibleInterfaces, got %v", err) }
}

func TestAdmitExplicitAllowlist(t *testing.T) {
    got, err := Admit([]ObservedInterface{{ID:"wifi",Up:true,HasRoute:true},{ID:"cellular",Up:true,HasRoute:true}}, Policy{ExplicitIDs:[]string{"cellular"},AllowMetered:true})
    if err != nil { t.Fatalf("Admit() error = %v", err) }
    if len(got)!=1 || got[0]!="cellular" { t.Fatalf("unexpected candidates: %#v", got) }
}

func TestAdmitRejectsDuplicateObservedIdentity(t *testing.T) {
    _, err := Admit([]ObservedInterface{{ID:"wifi",Up:true,HasRoute:true},{ID:" wifi ",Up:true,HasRoute:true}}, Policy{AllowMetered:true})
    if !errors.Is(err, ErrDuplicateInterface) { t.Fatalf("expected ErrDuplicateInterface, got %v", err) }
}

func TestAdmitRejectsDuplicateAllowlistIdentity(t *testing.T) {
    _, err := Admit([]ObservedInterface{{ID:"wifi",Up:true,HasRoute:true}}, Policy{ExplicitIDs:[]string{"wifi"," wifi "},AllowMetered:true})
    if !errors.Is(err, ErrDuplicateInterface) { t.Fatalf("expected ErrDuplicateInterface, got %v", err) }
}

func TestAdmitRejectsInvalidIdentity(t *testing.T) {
    for _, id := range []string{"","   ","wifi\ncellular"} {
        _, err := Admit([]ObservedInterface{{ID:id,Up:true,HasRoute:true}}, Policy{AllowMetered:true})
        if !errors.Is(err, ErrInvalidInterfaceID) { t.Fatalf("id %q: expected ErrInvalidInterfaceID, got %v", id, err) }
    }
}

func TestAdmitBoundsObservedInterfaces(t *testing.T) {
    items := make([]ObservedInterface, MaxObservedInterfaces+1)
    for i := range items { items[i]=ObservedInterface{ID:string(rune('a'+i)),Up:true,HasRoute:true} }
    _, err := Admit(items, Policy{AllowMetered:true})
    if !errors.Is(err, ErrTooManyInterfaces) { t.Fatalf("expected ErrTooManyInterfaces, got %v", err) }
}

func TestAdmitMaxActivePathsDeterministic(t *testing.T) {
    got, err := Admit([]ObservedInterface{{ID:"wifi",Up:true,HasRoute:true},{ID:"ethernet",Up:true,HasRoute:true},{ID:"cellular",Up:true,HasRoute:true}}, Policy{AllowMetered:true,MaxActivePaths:2})
    if err != nil { t.Fatalf("Admit() error = %v", err) }
    if len(got)!=2 || got[0]!="cellular" || got[1]!="ethernet" { t.Fatalf("unexpected bounded candidates: %#v", got) }
}

func TestAdmitMaxActivePathsAfterEligibility(t *testing.T) {
    got, err := Admit([]ObservedInterface{{ID:"cellular",Up:true,HasRoute:true,Metered:true},{ID:"wifi",Up:true,HasRoute:true},{ID:"ethernet",Up:false,HasRoute:true}}, Policy{MaxActivePaths:1})
    if err != nil { t.Fatalf("Admit() error = %v", err) }
    if len(got)!=1 || got[0]!="wifi" { t.Fatalf("unexpected candidates: %#v", got) }
}

func TestAdmitRejectsInvalidActiveLimit(t *testing.T) {
    for _, limit := range []int{-1, MaxObservedInterfaces+1} {
        _, err := Admit([]ObservedInterface{{ID:"wifi",Up:true,HasRoute:true}}, Policy{AllowMetered:true,MaxActivePaths:limit})
        if !errors.Is(err, ErrInvalidActiveLimit) { t.Fatalf("limit %d: expected ErrInvalidActiveLimit, got %v", limit, err) }
    }
}
