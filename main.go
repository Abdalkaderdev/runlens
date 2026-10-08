package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Abdalkaderdev/runlens/internal/launch"
	"github.com/Abdalkaderdev/runlens/internal/mcpserver"
	"github.com/Abdalkaderdev/runlens/internal/otlp"
	"github.com/Abdalkaderdev/runlens/internal/server"
	"github.com/Abdalkaderdev/runlens/internal/store"
	"github.com/Abdalkaderdev/runlens/internal/ui"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev"

const usage = `runlens: live traces, service map and MCP for your local dev server

Usage:
  runlens [flags] <command> [args...]   run a dev server under runlens
  runlens [flags]                       only receive OTLP from apps you start yourself
  runlens mcp [--url URL]               MCP over stdio, backed by a running runlens
  runlens version

Examples:
  runlens npm run dev
  runlens node server.js
  runlens uvicorn main:app --reload

Flags:
`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp":
			os.Exit(runMCP(os.Args[2:]))
		case "version", "--version", "-v":
			fmt.Println("runlens", version)
			return
		}
	}
	os.Exit(run())
}

func cacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "runlens")
}

func run() int {
	fs := flag.NewFlagSet("runlens", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage); fs.PrintDefaults() }
	port := fs.Int("port", 4318, "HTTP port for the UI, API, MCP and OTLP/HTTP; falls back to a free port if taken")
	grpcPort := fs.Int("grpc-port", 4317, "OTLP/gRPC port; 0 disables gRPC")
	noOpen := fs.Bool("no-open", false, "do not open the browser")
	showValues := fs.Bool("show-values", false, "keep SQL literals, query strings and auth headers instead of redacting them")
	slow := fs.Duration("slow", 500*time.Millisecond, "requests slower than this are flagged")
	slowQuery := fs.Duration("slow-query", 100*time.Millisecond, "DB and outbound spans slower than this are flagged")
	n1 := fs.Int("n1", 5, "same query shape this many times in one request counts as N+1")
	capacity := fs.Int("capacity", 2000, "traces kept in memory")
	rt := fs.String("runtime", "auto", "instrumentation to inject: auto, node, python or none")
	service := fs.String("service", "", "service name (default: current directory name)")
	fs.Parse(os.Args[1:])
	argv := fs.Args()

	opt := store.Options{NPlusOneMin: *n1, SlowMs: ms(*slow), SlowQueryMs: ms(*slowQuery)}
	st := store.New(*capacity)
	rc := &otlp.Receiver{Store: st, Redact: !*showValues}
	src := func(context.Context) ([][]*store.Span, error) { return st.Traces(), nil }
	srv := &server.Server{Store: st, Opt: opt, Receiver: rc, MCP: mcpserver.New(src, opt, version), UI: ui.FS(), Version: version}

	ln, err := listen(*port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runlens:", err)
		return 1
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	os.MkdirAll(cacheDir(), 0o755)
	os.WriteFile(filepath.Join(cacheDir(), "last-url"), []byte(base), 0o644)
	go (&http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}).Serve(ln)

	grpcAddr := "off"
	if *grpcPort != 0 {
		if gl, err := listen(*grpcPort); err == nil {
			grpcAddr = gl.Addr().String()
			go rc.ServeGRPC(gl)
		}
	}
	fmt.Fprintf(os.Stderr, "\n  runlens %s\n  ui    %s\n  mcp   %s/mcp\n  otlp  %s (http)  %s (grpc)\n\n", version, base, base, base, grpcAddr)

	if !*noOpen && os.Getenv("CI") == "" {
		openBrowser(base)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	if len(argv) == 0 {
		<-sig
		return 0
	}

	r := launch.Runtime(*rt)
	if *rt == "auto" {
		r = launch.Detect(argv)
	}
	name := *service
	if name == "" {
		wd, _ := os.Getwd()
		name = filepath.Base(wd)
	}
	extra, err := launch.Env(launch.Config{Runtime: r, Argv: argv, Endpoint: base, Service: name, CacheDir: cacheDir(), Log: os.Stderr})
	if err != nil {
		fmt.Fprintf(os.Stderr, "runlens: %v\nrunlens: starting without auto-instrumentation\n", err)
		r = launch.None
		extra = []string{"OTEL_EXPORTER_OTLP_ENDPOINT=" + base, "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf"}
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), extra...)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "runlens:", err)
		return 127
	}
	app := server.App{Command: strings.Join(argv, " "), Runtime: string(r), PID: cmd.Process.Pid, Running: true}
	srv.SetApp(app)

	done := make(chan int, 1)
	go func() {
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = 1
		}
		done <- code
	}()

	select {
	case code := <-done:
		app.Running, app.ExitCode = false, &code
		srv.SetApp(app)
		fmt.Fprintf(os.Stderr, "\nrunlens: app exited with code %d; UI stays up at %s, Ctrl+C to quit\n", code, base)
		<-sig
		return code
	case <-sig:
		if runtime.GOOS != "windows" {
			cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case code := <-done:
			return code
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			return 130
		}
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func listen(port int) (net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err == nil {
		return l, nil
	}
	l, err2 := net.Listen("tcp", "127.0.0.1:0")
	if err2 != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "runlens: port %d is busy, using %d\n", port, l.Addr().(*net.TCPAddr).Port)
	return l, nil
}

func openBrowser(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		c = exec.Command("open", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	c.Start()
}

func runMCP(args []string) int {
	fs := flag.NewFlagSet("runlens mcp", flag.ExitOnError)
	url := fs.String("url", "", "base URL of a running runlens (default: the last one started)")
	fs.Parse(args)
	if *url == "" {
		*url = "http://127.0.0.1:4318"
		if b, err := os.ReadFile(filepath.Join(cacheDir(), "last-url")); err == nil {
			*url = strings.TrimSpace(string(b))
		}
	}
	client := &http.Client{Timeout: 10 * time.Second}
	src := func(ctx context.Context) ([][]*store.Span, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, *url+"/api/export", nil)
		res, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("runlens is not running at %s; start it with `runlens <your dev command>`", *url)
		}
		defer res.Body.Close()
		var spans []*store.Span
		if err := json.NewDecoder(res.Body).Decode(&spans); err != nil {
			return nil, err
		}
		st := store.New(1 << 30)
		st.Add(spans)
		return st.Traces(), nil
	}
	opt := store.Defaults
	var meta struct {
		SlowMs      float64 `json:"slowMs"`
		SlowQueryMs float64 `json:"slowQueryMs"`
		NPlusOneMin int     `json:"nPlusOneMin"`
	}
	if res, err := client.Get(*url + "/api/meta"); err == nil {
		if json.NewDecoder(res.Body).Decode(&meta) == nil && meta.NPlusOneMin > 0 {
			opt = store.Options{NPlusOneMin: meta.NPlusOneMin, SlowMs: meta.SlowMs, SlowQueryMs: meta.SlowQueryMs}
		}
		res.Body.Close()
	}
	if err := mcpserver.New(src, opt, version).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "runlens mcp:", err)
		return 1
	}
	return 0
}
