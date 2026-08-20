//go:build integration

package testenv

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	gopath "path"
	"strings"
	"testing"
)

// ForgeStub is an in-process HTTP server that returns GitHub- and GitLab-shaped
// JSON bodies, so the go-github / gitlab-client fetchers can be driven through a
// real HTTP layer without touching the network. It serves a small in-memory
// repository: registered file paths return 200 with the right shape, everything
// else returns a forge-shaped 404, and setting Unauthorized makes every request
// return 401 (to exercise the 401-detection paths).
//
// GitLab fetchers accept a base URL, so point them at Server.URL. GitHub's
// client hard-codes api.github.com, so use GitHubHTTPClient(), whose transport
// rewrites requests to this server.
type ForgeStub struct {
	Server *httptest.Server

	// files maps a repo-relative path to its content. Registered paths return
	// 200; unregistered paths return a forge-shaped 404.
	files map[string][]byte
	// dirs maps a directory path to the entry names directly under it.
	dirs map[string][]string
	// refs is the set of resolvable refs (branch/tag/sha) -> resolved sha.
	refs map[string]string

	// Unauthorized, when true, makes every endpoint answer 401.
	Unauthorized bool
}

// NewForgeStub starts a forge stand-in and registers it for cleanup.
func NewForgeStub(t *testing.T) *ForgeStub {
	t.Helper()
	f := &ForgeStub{
		files: make(map[string][]byte),
		dirs:  make(map[string][]string),
		refs:  map[string]string{"main": "0000000000000000000000000000000000000001"},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Server.Close)
	return f
}

// AddFile registers a file (and its parent directory entry) in the stub repo.
func (f *ForgeStub) AddFile(path string, content []byte) {
	f.files[path] = content
	if i := strings.LastIndex(path, "/"); i >= 0 {
		dir := path[:i]
		f.dirs[dir] = append(f.dirs[dir], path[i+1:])
	} else {
		f.dirs[""] = append(f.dirs[""], path)
	}
}

// GitHubHTTPClient returns an *http.Client whose transport rewrites every
// request onto this server, so a go-github client (which targets
// api.github.com) reaches the stub instead.
func (f *ForgeStub) GitHubHTTPClient() *http.Client {
	target, _ := url.Parse(f.Server.URL)
	return &http.Client{Transport: &rewriteTransport{target: target}}
}

type rewriteTransport struct{ target *url.URL }

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	req.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func (f *ForgeStub) handle(w http.ResponseWriter, r *http.Request) {
	if f.Unauthorized {
		f.writeGitHubError(w, http.StatusUnauthorized, "Bad credentials")
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v4/"):
		f.handleGitLab(w, r)
	default:
		f.handleGitHub(w, r)
	}
}

// ----- GitHub -----

func (f *ForgeStub) handleGitHub(w http.ResponseWriter, r *http.Request) {
	// /repos/{owner}/{repo}/contents/{path}
	if idx := strings.Index(r.URL.Path, "/contents/"); idx >= 0 {
		path := strings.TrimPrefix(r.URL.Path[idx+len("/contents/"):], "/")
		f.serveGitHubContents(w, path)
		return
	}
	// Ref resolution endpoints: commits/branches/git refs. We only resolve
	// refs registered in f.refs; everything else is a 404 (drives
	// ErrRemoteContentNotFound through ResolveRef).
	if ref, ok := f.githubRef(r.URL.Path); ok {
		if sha, found := f.refs[ref]; found {
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "name": ref})
			return
		}
	}
	f.writeGitHubError(w, http.StatusNotFound, "Not Found")
}

func (f *ForgeStub) githubRef(path string) (string, bool) {
	for _, marker := range []string{"/commits/", "/branches/", "/git/refs/tags/", "/git/ref/tags/"} {
		if i := strings.Index(path, marker); i >= 0 {
			return strings.TrimPrefix(path[i+len(marker):], "/"), true
		}
	}
	return "", false
}

func (f *ForgeStub) serveGitHubContents(w http.ResponseWriter, path string) {
	if content, ok := f.files[path]; ok {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type":     "file",
			"encoding": "base64",
			"size":     len(content),
			"name":     gopath.Base(path),
			"path":     path,
			"sha":      "filesha",
			"content":  base64.StdEncoding.EncodeToString(content),
		})
		return
	}
	if names, ok := f.dirs[path]; ok {
		entries := make([]map[string]any, 0, len(names))
		for _, name := range names {
			child := name
			if path != "" {
				child = path + "/" + name
			}
			typ := "file"
			if _, isDir := f.dirs[child]; isDir {
				typ = "dir"
			}
			entries = append(entries, map[string]any{
				"type": typ, "name": name, "path": child, "sha": "entrysha",
			})
		}
		_ = json.NewEncoder(w).Encode(entries)
		return
	}
	f.writeGitHubError(w, http.StatusNotFound, "Not Found")
}

func (f *ForgeStub) writeGitHubError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message":           message,
		"documentation_url": "https://docs.github.com/rest",
	})
}

// ----- GitLab -----

func (f *ForgeStub) handleGitLab(w http.ResponseWriter, r *http.Request) {
	// /api/v4/projects/{id}/repository/files/{filepath}/raw
	if i := strings.Index(r.URL.Path, "/repository/files/"); i >= 0 {
		rest := r.URL.Path[i+len("/repository/files/"):]
		rest = strings.TrimSuffix(rest, "/raw")
		path, _ := url.PathUnescape(rest)
		if content, ok := f.files[path]; ok {
			_, _ = w.Write(content)
			return
		}
		f.writeGitLabError(w, http.StatusNotFound, "404 File Not Found")
		return
	}
	// /api/v4/projects/{id}/repository/tree
	if strings.Contains(r.URL.Path, "/repository/tree") {
		path := r.URL.Query().Get("path")
		if names, ok := f.dirs[path]; ok {
			entries := make([]map[string]any, 0, len(names))
			for _, name := range names {
				child := name
				if path != "" {
					child = path + "/" + name
				}
				typ := "blob"
				if _, isDir := f.dirs[child]; isDir {
					typ = "tree"
				}
				entries = append(entries, map[string]any{
					"id": "treesha", "name": name, "path": child, "type": typ,
				})
			}
			_ = json.NewEncoder(w).Encode(entries)
			return
		}
		f.writeGitLabError(w, http.StatusNotFound, "404 Tree Not Found")
		return
	}
	// Ref resolution: commits/branches/tags.
	if ref, ok := f.gitlabRef(r.URL.Path); ok {
		if sha, found := f.refs[ref]; found {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": sha, "name": ref})
			return
		}
	}
	f.writeGitLabError(w, http.StatusNotFound, "404 Not Found")
}

func (f *ForgeStub) gitlabRef(path string) (string, bool) {
	for _, marker := range []string{"/repository/commits/", "/repository/branches/", "/repository/tags/"} {
		if i := strings.Index(path, marker); i >= 0 {
			ref, _ := url.PathUnescape(strings.TrimPrefix(path[i+len(marker):], "/"))
			return ref, true
		}
	}
	return "", false
}

func (f *ForgeStub) writeGitLabError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"message": message})
}
