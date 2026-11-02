package fakegateway

import "testing"

// Start runs a server for the test and returns it with its base URL.
func Start(t testing.TB, respond Responder) (*Server, string) {
	t.Helper()
	s := New(respond)
	url, stop, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return s, url
}
