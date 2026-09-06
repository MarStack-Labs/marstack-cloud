package app

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

const consolePolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'"

func consoleHandler(dir string) http.Handler {
	return consoleFiles(os.DirFS(dir))
}

func consoleFiles(root fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "the console only answers GET and HEAD",
				http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" || name == "." {
			name = "index.html"
		}
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}

		file, err := root.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()

		info, err := file.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}

		seeker, ok := file.(io.ReadSeeker)
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Security-Policy", consolePolicy)
		http.ServeContent(w, r, info.Name(), info.ModTime(), seeker)
	})
}
