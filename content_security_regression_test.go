package reviews_test

import (
	"context"
	"encoding/json"
	r "github.com/StuHelper/stuhelper-reviews"
	"testing"
)

func TestVoteReceiptsDoNotExposeTargetContentAfterAdmissionExpires(t *testing.T) {
	f := fixture(t)
	ctx := context.Background()
	entry := create(t, f, "vote-target", input("course", "autumn", 4))
	voter := r.Principal{Subject: "voter"}
	if _, e := f.service.Action(ctx, voter, entry.ID, "vote", r.Action{Action: "vote", Vote: 1}); e != nil {
		t.Fatal(e)
	}
	f.access.revoked = true
	votes, e := f.service.MyVotes(ctx, voter, "", 20)
	if e != nil {
		t.Fatal(e)
	}
	data, e := json.Marshal(votes)
	if e != nil {
		t.Fatal(e)
	}
	var rows []map[string]any
	if e = json.Unmarshal(data, &rows); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0]["id"] != entry.ID || rows[0]["my_vote"] != float64(1) {
		t.Fatal("own vote receipt lost")
	}
	for _, key := range []string{"body", "title", "author", "ratings", "upvotes", "downvotes"} {
		if _, found := rows[0][key]; found {
			t.Fatalf("vote receipt exposes another author's content: %s", key)
		}
	}
}
