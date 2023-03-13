package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinas/alice"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"github.com/rs/zerolog/log"
)

var (
	logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	flags  = struct {
		// Data directory for the cache
		DataDir string
		// Port to listen on
		ListenPort string
	}{}
)

func run() error {
	flag.StringVar(&flags.DataDir, "data-dir", "./data", "Data dir for the cache")
	flag.StringVar(&flags.ListenPort, "port", ":8090", "Port to listen on")
	flag.Parse()

	c := alice.New()

	// Install the logger handler with default output on the console
	c = c.Append(hlog.NewHandler(logger))

	// Install some provided extra handler to set some request's context fields.
	// Thanks to that handler, all our logs will come with some prepopulated fields.
	c = c.Append(hlog.AccessHandler(func(r *http.Request, status, size int, duration time.Duration) {
		hlog.FromRequest(r).Info().
			Str("method", r.Method).
			Stringer("url", r.URL).
			Int("status", status).
			Int("size", size).
			Dur("duration", duration).
			Msg("")
	}))
	c = c.Append(hlog.RemoteAddrHandler("ip"))
	c = c.Append(hlog.UserAgentHandler("user_agent"))
	c = c.Append(hlog.RefererHandler("referer"))
	c = c.Append(hlog.RequestIDHandler("req_id", "Request-Id"))

	// Here is your final handler
	h := c.Then(http.HandlerFunc(handleSourceGo))
	http.Handle("/source/go", h)

	if err := http.ListenAndServe(":8090", nil); err != nil {
		return err
	}

	return nil
}

func goEnv() []string {
	cacheDir, err := filepath.Abs(flags.DataDir)
	if err != nil {
		panic(err)
	}

	return []string{
		"GOPATH=" + filepath.Join(cacheDir, "go-mod-cache"),
		"GOCACHE=" + filepath.Join(cacheDir, "go-cache"),
		"GOMODCACHE=" + filepath.Join(cacheDir, "go-mod-cache"),
	}

}

func writeSourceGo(ctx context.Context, repo, revision, sourcePath string, w io.Writer) error {

	// handle if the source path is a go mod dependency
	pkgModPath := "/pkg/mod/"
	if i := strings.Index(sourcePath, pkgModPath); i > 0 {
		depPath := sourcePath[len(pkgModPath)+i:]

		posAt := strings.Index(depPath, "@")
		if posAt > 0 {
			posEnd := posAt + strings.Index(depPath[posAt:], "/")
			if posEnd > 0 {
				module := depPath[:posAt]
				revision := depPath[posAt+1 : posEnd]
				log.Ctx(ctx).Info().
					Str("module", module).
					Str("revision", revision).
					Msg("this is acutally for a dependecy source code")

				return writeSourceGo(ctx, module, revision, depPath[posEnd:], w)
			}
		}
	}

	cmd := []string{"go", "mod", "download", "-json", repo + "@" + revision}
	log.Ctx(ctx).Info().
		Strs("cmd", cmd).
		Msg("running command")
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = append(os.Environ(), goEnv()...)

	var stdOut, stdErr bytes.Buffer
	c.Stdout = &stdOut
	c.Stderr = &stdErr
	exitErr := c.Run()

	goModInfo := struct {
		Path     string `json:"Path"`
		Version  string `json:"Version"`
		Query    string `json:"Query"`
		Info     string `json:"Info"`
		GoMod    string `json:"GoMod"`
		Zip      string `json:"Zip"`
		Dir      string `json:"Dir"`
		Sum      string `json:"Sum"`
		GoModSum string `json:"GoModSum"`
	}{}

	if err := json.Unmarshal(stdOut.Bytes(), &goModInfo); err != nil {
		return err
	}

	// TODO: Figure out if this should be an error after all
	if exitErr != nil {
		return fmt.Errorf("%+#v: %w", goModInfo, exitErr)
	}

	log.Ctx(ctx).Info().
		Str("repo", goModInfo.Path).
		Str("local-path", goModInfo.Dir).
		Str("version", goModInfo.Version).
		Msg("found go module")

	// now find the file with longest prefix match (only works for files within repo)
	foundFile := ""
	prefixLen := len(goModInfo.Dir)
	if err := filepath.WalkDir(goModInfo.Dir, func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}

		relPath := path[prefixLen+1:]

		if strings.HasSuffix(sourcePath, relPath) {
			fmt.Println("found", relPath)
			if len(foundFile) < len(relPath) {
				foundFile = relPath
			}
		}

		return nil
	}); err != nil {
		return err
	}

	if foundFile == "" {
		return io.EOF
	}

	f, err := os.Open(filepath.Join(goModInfo.Dir, foundFile))
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(w, f); err != nil {
		return err
	}

	return nil
}

func handleSourceGo(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	var (
		repo     = q.Get("repo")
		revision = q.Get("revision")
		path     = q.Get("path")
	)

	if repo == "" {
		http.Error(w, "missing repo", http.StatusBadRequest)
		return
	}

	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	if revision == "" {
		revision = "latest"
	}

	if err := writeSourceGo(req.Context(), repo, revision, path, w); err != nil {
		http.Error(w, "error receiving source code", http.StatusInternalServerError)
		hlog.FromRequest(req).Error().Err(err).Msg("error receiving source code")
		return
	}

	fmt.Fprintf(w, "hello\n")
}

func main() {
	if err := run(); err != nil {
		log.Fatal().Err(err).Msg("Error")
	}
}
