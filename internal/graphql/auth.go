package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// ErrInvalidCredentials is returned by Login when the server rejects the password.
var ErrInvalidCredentials = errors.New("invalid credentials")

const queryLogin = `
mutation Login($name: String!, $password: String!) {
  login(name: $name, password: $password) {
    me { username }
  }
}`

const queryLogout = `
mutation { logout { me { username } } }`

const queryMe = `
{ me { username } }`

// Login authenticates with name/password. On success the session cookie is
// stored in the cookie jar and attached to all subsequent requests.
// Returns the logged-in username.
func (c *Client) Login(ctx context.Context, name, password string) (string, error) {
	var out struct {
		Login *struct {
			Me *struct {
				Username string `json:"username"`
			} `json:"me"`
		} `json:"login"`
	}
	vars := map[string]any{"name": name, "password": password}
	if err := c.Do(ctx, queryLogin, vars, &out); err != nil {
		return "", err
	}
	if out.Login == nil || out.Login.Me == nil {
		return "", ErrInvalidCredentials
	}
	return out.Login.Me.Username, nil
}

// Me returns the username of the currently authenticated user, or "" if anonymous.
func (c *Client) Me(ctx context.Context) (string, error) {
	var out struct {
		Me *struct {
			Username string `json:"username"`
		} `json:"me"`
	}
	if err := c.Do(ctx, queryMe, nil, &out); err != nil {
		return "", err
	}
	if out.Me == nil {
		return "", nil
	}
	return out.Me.Username, nil
}

// Logout invalidates the current session on the server and clears local cookies.
func (c *Client) Logout(ctx context.Context) error {
	err := c.Do(ctx, queryLogout, nil, nil)
	// Even if the server call fails, clear local cookies — caller likely wants
	// to wipe the session regardless.
	c.clearCookies()
	return err
}

func (c *Client) clearCookies() {
	if c.jar == nil {
		return
	}
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return
	}
	cookies := c.jar.Cookies(u)
	for i := range cookies {
		cookies[i].MaxAge = -1
		cookies[i].Expires = time.Unix(1, 0)
	}
	c.jar.SetCookies(u, cookies)
}

// --- Session persistence -----------------------------------------------------

type savedCookie struct {
	Name     string    `json:"n"`
	Value    string    `json:"v"`
	Path     string    `json:"p,omitempty"`
	Domain   string    `json:"d,omitempty"`
	Expires  time.Time `json:"e,omitempty"`
	Secure   bool      `json:"s,omitempty"`
	HttpOnly bool      `json:"h,omitempty"`
}

// SaveSession writes the current session cookies for the GraphQL endpoint to
// path (creating parent dirs as needed). Use 0600 perms — the cookie is as
// sensitive as a password.
func (c *Client) SaveSession(path string) error {
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return err
	}
	cookies := c.jar.Cookies(u)
	saved := make([]savedCookie, len(cookies))
	for i, ck := range cookies {
		saved[i] = savedCookie{
			Name:     ck.Name,
			Value:    ck.Value,
			Path:     ck.Path,
			Domain:   ck.Domain,
			Expires:  ck.Expires,
			Secure:   ck.Secure,
			HttpOnly: ck.HttpOnly,
		}
	}
	b, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// LoadSession restores session cookies previously written by SaveSession.
// Missing file is not an error (returns nil) — caller is anonymous.
func (c *Client) LoadSession(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var saved []savedCookie
	if err := json.Unmarshal(b, &saved); err != nil {
		return err
	}
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return err
	}
	cookies := make([]*http.Cookie, len(saved))
	for i, s := range saved {
		cookies[i] = &http.Cookie{
			Name:     s.Name,
			Value:    s.Value,
			Path:     s.Path,
			Domain:   s.Domain,
			Expires:  s.Expires,
			Secure:   s.Secure,
			HttpOnly: s.HttpOnly,
		}
	}
	c.jar.SetCookies(u, cookies)
	return nil
}
