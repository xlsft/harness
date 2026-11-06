package sshhost

import "net/http"

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func httpClient(token string) *http.Client {
	return &http.Client{Transport: bearerTransport{token}}
}
