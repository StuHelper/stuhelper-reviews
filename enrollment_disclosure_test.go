package reviews

import "testing"

func TestEnrollmentIsServerSnapshotOnly(t *testing.T) {
	if validDisclosureKeys([]string{"enrollment"}) {
		t.Fatal("author can submit enrollment as a disclosure")
	}
	if !validDisclosureKeys([]string{"gender", "real_name"}) {
		t.Fatal("unrelated public attributes rejected")
	}
}
