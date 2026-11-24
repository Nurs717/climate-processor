package access

import "testing"

func TestRequestsDeduplicate(t *testing.T) {
	r := NewRequests()

	if !r.Add(Request{ChatID: 1, Name: "Wife"}) {
		t.Fatal("first request must be accepted")
	}
	if r.Add(Request{ChatID: 1, Name: "Wife again"}) {
		t.Error("a second request from the same chat must be ignored while the first one waits")
	}
	if !r.Add(Request{ChatID: 2, Name: "Friend"}) {
		t.Error("a request from another chat must be accepted")
	}
}

func TestRequestsTake(t *testing.T) {
	r := NewRequests()
	r.Add(Request{ChatID: 1, Name: "Wife"})

	req, ok := r.Take(1)
	if !ok || req.Name != "Wife" {
		t.Fatalf("Take = (%+v, %v)", req, ok)
	}
	if _, ok := r.Take(1); ok {
		t.Error("a request can be taken only once (a double click must not decide twice)")
	}
	// After the decision the person may ask again (for example after being removed later).
	if !r.Add(Request{ChatID: 1, Name: "Wife"}) {
		t.Error("a new request after a decision must be accepted")
	}
}
