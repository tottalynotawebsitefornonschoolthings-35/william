package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/sessions"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
)

func testGitHubProvider(hostname string) *GitHubProvider {
	p := NewGitHubProvider(
		&ProviderData{
			ProviderName: "",
			LoginURL:     &url.URL{},
			RedeemURL:    &url.URL{},
			ProfileURL:   &url.URL{},
			ValidateURL:  &url.URL{},
			Scope:        ""})
	if hostname != "" {
		updateURL(p.Data().LoginURL, hostname)
		updateURL(p.Data().RedeemURL, hostname)
		updateURL(p.Data().ProfileURL, hostname)
		updateURL(p.Data().ValidateURL, hostname)
	}
	return p
}

func testGitHubBackend(payloads map[string][]string) *httptest.Server {
	pathToQueryMap := map[string][]string{
		"/repo/oauth2-proxy/oauth2-proxy":                       {""},
		"/repos/oauth2-proxy/oauth2-proxy/collaborators/mbland": {""},
		"/user":        {""},
		"/user/emails": {""},
		"/user/orgs":   {"page=1&per_page=100", "page=2&per_page=100", "page=3&per_page=100"},
	}

	return httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			query, ok := pathToQueryMap[r.URL.Path]
			validQuery := false
			index := 0
			for i, q := range query {
				if q == r.URL.RawQuery {
					validQuery = true
					index = i
				}
			}
			payload := []string{}
			if ok && validQuery {
				payload, ok = payloads[r.URL.Path]
			}
			if !ok {
				w.WriteHeader(404)
			} else if !validQuery {
				w.WriteHeader(404)
			} else if payload[index] == "" {
				w.WriteHeader(204)
			} else {
				w.WriteHeader(200)
				w.Write([]byte(payload[index]))
			}
		}))
}

func TestNewGitHubProvider(t *testing.T) {
	g := NewWithT(t)

	// Test that defaults are set when calling for a new provider with nothing set
	providerData := NewGitHubProvider(&ProviderData{}).Data()
	g.Expect(providerData.ProviderName).To(Equal("GitHub"))
	g.Expect(providerData.LoginURL.String()).To(Equal("https://github.com/login/oauth/authorize"))
	g.Expect(providerData.RedeemURL.String()).To(Equal("https://github.com/login/oauth/access_token"))
	g.Expect(providerData.ProfileURL.String()).To(Equal(""))
	g.Expect(providerData.ValidateURL.String()).To(Equal("https://api.github.com/"))
	g.Expect(providerData.Scope).To(Equal("user:email"))
}

func TestGitHubProviderOverrides(t *testing.T) {
	p := NewGitHubProvider(
		&ProviderData{
			LoginURL: &url.URL{
				Scheme: "https",
				Host:   "example.com",
				Path:   "/login/oauth/authorize"},
			RedeemURL: &url.URL{
				Scheme: "https",
				Host:   "example.com",
				Path:   "/login/oauth/access_token"},
			ValidateURL: &url.URL{
				Scheme: "https",
				Host:   "api.example.com",
				Path:   "/"},
			Scope: "profile"})
	assert.NotEqual(t, nil, p)
	assert.Equal(t, "GitHub", p.Data().ProviderName)
	assert.Equal(t, "https://example.com/login/oauth/authorize",
		p.Data().LoginURL.String())
	assert.Equal(t, "https://example.com/login/oauth/access_token",
		p.Data().RedeemURL.String())
	assert.Equal(t, "https://api.example.com/",
		p.Data().ValidateURL.String())
	assert.Equal(t, "profile", p.Data().Scope)
}

func TestGitHubProvider_getEmail(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user/emails": {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}

func TestGitHubProvider_getEmailNotVerified(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user/emails": {`[ {"email": "michael.bland@gsa.gov", "verified": false, "primary": true} ]`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Empty(t, session.Email)
}

func TestGitHubProvider_getEmailWithOrg(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user/emails": {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
		"/user/orgs": {
			`[ {"login":"testorg"} ]`,
			`[ {"login":"testorg1"} ]`,
			`[ ]`,
		},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.Org = "testorg1"

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}

func TestGitHubProvider_getEmailWithWriteAccessToPublicRepo(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/repo/oauth2-proxy/oauth2-proxy": {`{"permissions": {"pull": true, "push": true}, "private": false}`},
		"/user/emails":                    {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetRepo("oauth2-proxy/oauth2-proxy", "")

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}

func TestGitHubProvider_getEmailWithReadOnlyAccessToPrivateRepo(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/repo/oauth2-proxy/oauth2-proxy": {`{"permissions": {"pull": true, "push": false}, "private": true}`},
		"/user/emails":                    {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetRepo("oauth2-proxy/oauth2-proxy", "")

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}

func TestGitHubProvider_getEmailWithWriteAccessToPrivateRepo(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/repo/oauth2-proxy/oauth2-proxy": {`{"permissions": {"pull": true, "push": true}, "private": true}`},
		"/user/emails":                    {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetRepo("oauth2-proxy/oauth2-proxy", "")

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}

func TestGitHubProvider_getEmailWithNoAccessToPrivateRepo(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/repo/oauth2-proxy/oauth2-proxy": {`{}`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetRepo("oauth2-proxy/oauth2-proxy", "")

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Empty(t, session.Email)
}

func TestGitHubProvider_getEmailWithToken(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user/emails": {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetRepo("oauth2-proxy/oauth2-proxy", "token")

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}

// Note that trying to trigger the "failed building request" case is not
// practical, since the only way it can fail is if the URL fails to parse.
func TestGitHubProvider_getEmailFailedRequest(t *testing.T) {
	b := testGitHubBackend(map[string][]string{})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)

	// We'll trigger a request failure by using an unexpected access
	// token. Alternatively, we could allow the parsing of the payload as
	// JSON to fail.
	session := &sessions.SessionState{AccessToken: "unexpected_access_token"}
	err := p.getEmail(context.Background(), session)
	assert.Error(t, err)
	assert.Empty(t, session.Email)
}

func TestGitHubProvider_getEmailNotPresentInPayload(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user/emails": {`{"foo": "bar"}`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.Error(t, err)
	assert.Empty(t, session.Email)
}

func TestGitHubProvider_getUser(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user": {`{"email": "michael.bland@gsa.gov", "login": "mbland"}`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)

	session := CreateAuthorizedSession()
	err := p.getUser(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "mbland", session.User)
}

func TestGitHubProvider_getUserWithRepoAndToken(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user": {`{"email": "michael.bland@gsa.gov", "login": "mbland"}`},
		"/repos/oauth2-proxy/oauth2-proxy/collaborators/mbland": {""},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetRepo("oauth2-proxy/oauth2-proxy", "token")

	session := CreateAuthorizedSession()
	err := p.getUser(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "mbland", session.User)
}

func TestGitHubProvider_getUserWithRepoAndTokenWithoutPushAccess(t *testing.T) {
	b := testGitHubBackend(map[string][]string{})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetRepo("oauth2-proxy/oauth2-proxy", "token")

	session := CreateAuthorizedSession()
	err := p.getUser(context.Background(), session)
	assert.Error(t, err)
	assert.Empty(t, session.User)
}

func TestGitHubProvider_getEmailWithUsername(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user":        {`{"email": "michael.bland@gsa.gov", "login": "mbland"}`},
		"/user/emails": {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetUsers([]string{"mbland", "octocat"})

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}

func TestGitHubProvider_getEmailWithNotAllowedUsername(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user":        {`{"email": "michael.bland@gsa.gov", "login": "mbland"}`},
		"/user/emails": {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetUsers([]string{"octocat"})

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.Error(t, err)
	assert.Empty(t, session.Email)
}

func TestGitHubProvider_getEmailWithUsernameAndNotBelongToOrg(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user":        {`{"email": "michael.bland@gsa.gov", "login": "mbland"}`},
		"/user/emails": {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
		"/user/orgs": {
			`[ {"login":"testorg"} ]`,
			`[ ]`,
		},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetOrgTeam("not_belong_to", "")
	p.SetUsers([]string{"mbland"})

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}

func TestGitHubProvider_getEmailWithUsernameAndNoAccessToPrivateRepo(t *testing.T) {
	b := testGitHubBackend(map[string][]string{
		"/user":                           {`{"email": "michael.bland@gsa.gov", "login": "mbland"}`},
		"/user/emails":                    {`[ {"email": "michael.bland@gsa.gov", "verified": true, "primary": true} ]`},
		"/repo/oauth2-proxy/oauth2-proxy": {`{}`},
	})
	defer b.Close()

	bURL, _ := url.Parse(b.URL)
	p := testGitHubProvider(bURL.Host)
	p.SetRepo("oauth2-proxy/oauth2-proxy", "")
	p.SetUsers([]string{"mbland"})

	session := CreateAuthorizedSession()
	err := p.getEmail(context.Background(), session)
	assert.NoError(t, err)
	assert.Equal(t, "michael.bland@gsa.gov", session.Email)
}
