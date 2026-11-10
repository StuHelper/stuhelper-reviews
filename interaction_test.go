package reviews_test

import (
	"context"
	"errors"
	r "github.com/StuHelper/stuhelper-reviews"
	"sync"
	"testing"
)

func TestExpiredAdmissionAllowsOnlyOwnVoteRemoval(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	entry := create(t, f, "interactive", input("course", "autumn", 4))
	voter := r.Principal{Subject: "voter"}
	other := r.Principal{Subject: "other"}
	if _, e := f.service.Action(ctx, r.Principal{Subject: "member"}, entry.ID, "denied-vote", r.Action{Action: "vote", Vote: 1}); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("unverified vote accepted", e)
	}

	for _, p := range []r.Principal{voter, other} {
		if _, e := f.service.Action(ctx, p, entry.ID, "vote", r.Action{Action: "vote", Vote: 1}); e != nil {
			t.Fatal(e)
		}

	}
	f.access.revoked = true
	if _, e := f.service.Action(ctx, voter, entry.ID, "switch", r.Action{Action: "vote", Vote: -1}); !errors.Is(e, r.ErrForbidden) {
		t.Fatal("changed vote after expiry", e)
	}

	votes, e := f.service.MyVotes(ctx, voter, "", 20)
	if e != nil || len(votes) != 1 || votes[0].MyVote != 1 {
		t.Fatal("lost prior vote", votes, e)
	}

	// Cancellation is local: expired qualification and unavailable target facts cannot block it.
	f.access.failed = true
	f.catalog.failed = true
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.service.Action(ctx, voter, entry.ID, "cancel-vote", r.Action{Action: "vote", Vote: 0})
			errs <- e

		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal("own cancellation blocked", e)
		}
	}
	votes, e = f.service.MyVotes(ctx, voter, "", 20)
	if e != nil || len(votes) != 0 {
		t.Fatal("vote persisted", votes, e)
	}

	votes, e = f.service.MyVotes(ctx, other, "", 20)
	if e != nil || len(votes) != 1 || votes[0].MyVote != 1 {
		t.Fatal("changed another user vote", votes, e)
	}

	if _, e = f.service.Action(ctx, voter, entry.ID, "cancel-vote", r.Action{Action: "vote", Vote: 1}); !errors.Is(e, r.ErrConflict) {
		t.Fatal("changed replay payload accepted", e)
	}
}
