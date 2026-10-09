package launch

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Runtime string

const (
	Node   Runtime = "node"
	Python Runtime = "python"
	None   Runtime = "none"
)

var (
	//go:embed assets/package.json
	nodePackage []byte
	//go:embed assets/register.mjs
	nodeRegister []byte
	//go:embed assets/sitecustomize.py
	pySitecustomize []byte
	//go:embed assets/requirements.txt
	pyRequirements []byte
)

var nodeCmds = map[string]bool{"node": true, "nodejs": true, "npm": true, "npx": true, "pnpm": true, "pnpx": true, "yarn": true, "tsx": true, "ts-node": true, "nodemon": true, "next": true, "nest": true, "vite": true, "remix": true, "astro": true, "nuxt": true}

var pyCmds = map[string]bool{"py": true, "uv": true, "poetry": true, "pipenv": true, "pdm": true, "hatch": true, "uvicorn": true, "gunicorn": true, "hypercorn": true, "granian": true, "daphne": true, "flask": true, "fastapi": true, "django-admin": true, "celery": true}

func base(cmd string) string {
	b := strings.ToLower(filepath.Base(cmd))
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1"} {
		b = strings.TrimSuffix(b, ext)
	}
	return b
}

func Detect(argv []string) Runtime {
	if len(argv) == 0 {
		return None
	}
	b := base(argv[0])
	switch {
	case nodeCmds[b]:
		return Node
	case pyCmds[b], strings.HasPrefix(b, "python"), strings.HasSuffix(b, ".py"):
		return Python
	}
	return None
}

type Config struct {
	Runtime  Runtime
	Argv     []string
	Endpoint string
	Service  string
	CacheDir string
	Log      io.Writer
}

func Env(c Config) ([]string, error) {
	env := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": c.Endpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
	}
	defaults := map[string]string{
		"OTEL_SERVICE_NAME":       c.Service,
		"OTEL_METRICS_EXPORTER":   "none",
		"OTEL_LOGS_EXPORTER":      "none",
		"OTEL_BSP_SCHEDULE_DELAY": "200",
		"OTEL_TRACES_SAMPLER":     "always_on",
	}
	switch c.Runtime {
	case Node:
		reg, err := setupNode(c)
		if err != nil {
			return nil, err
		}
		opt := "--import=" + (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(reg), "/")}).String()
		env["NODE_OPTIONS"] = strings.TrimSpace(os.Getenv("NODE_OPTIONS") + " " + opt)
		env["OTEL_TRACES_EXPORTER"] = "otlp"
		defaults["OTEL_NODE_RESOURCE_DETECTORS"] = "env,host,process"
		defaults["OTEL_NODE_DISABLED_INSTRUMENTATIONS"] = "fs,dns,net"
	case Python:
		boot, site, err := setupPython(c)
		if err != nil {
			return nil, err
		}
		env["PYTHONPATH"] = joinPath(boot, os.Getenv("PYTHONPATH"))
		env["RUNLENS_PY_SITE"] = site
		env["RUNLENS_OTLP_ENDPOINT"] = c.Endpoint
		delete(env, "OTEL_EXPORTER_OTLP_ENDPOINT")
		env["OTEL_TRACES_EXPORTER"] = "otlp_proto_http"
	}
	for k, v := range defaults {
		if _, set := os.LookupEnv(k); !set {
			env[k] = v
		}
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out, nil
}

func joinPath(a, b string) string {
	if b == "" {
		return a
	}
	return a + string(os.PathListSeparator) + b
}

func setupNode(c Config) (string, error) {
	dir := filepath.Join(c.CacheDir, "node", stamp(nodePackage, nodeRegister))
	reg := filepath.Join(dir, "register.mjs")
	if _, err := os.Stat(filepath.Join(dir, ".ok")); err == nil {
		return reg, nil
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return "", fmt.Errorf("npm not found on PATH; it is needed once to install the OpenTelemetry Node packages")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), nodePackage, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(reg, nodeRegister, 0o644); err != nil {
		return "", err
	}
	fmt.Fprintf(c.Log, "runlens: installing Node instrumentation into %s (first run only)\n", dir)
	cmd := exec.Command(npm, "install", "--no-audit", "--no-fund", "--loglevel=error", "--ignore-scripts")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NODE_OPTIONS=")
	cmd.Stdout, cmd.Stderr = c.Log, c.Log
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("npm install in %s: %w", dir, err)
	}
	return reg, os.WriteFile(filepath.Join(dir, ".ok"), nil, 0o644)
}

func setupPython(c Config) (boot, site string, err error) {
	py, ver, err := findPython(c.Argv)
	if err != nil {
		return "", "", err
	}
	root := filepath.Join(c.CacheDir, "python")
	boot = filepath.Join(root, "boot-"+stamp(pySitecustomize))
	site = filepath.Join(root, ver+"-"+runtime.GOOS+"-"+stamp(pyRequirements), "site")
	if err := os.MkdirAll(boot, 0o755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(boot, "sitecustomize.py"), pySitecustomize, 0o644); err != nil {
		return "", "", err
	}
	ok := filepath.Join(filepath.Dir(site), ".ok")
	if _, err := os.Stat(ok); err == nil {
		return boot, site, nil
	}
	pkgs := strings.Fields(string(pyRequirements))
	fmt.Fprintf(c.Log, "runlens: installing Python %s instrumentation into %s (first run only)\n", ver, site)
	var cmd *exec.Cmd
	if uv, err := exec.LookPath("uv"); err == nil {
		cmd = exec.Command(uv, append([]string{"pip", "install", "--quiet", "--target", site, "--python", py}, pkgs...)...)
	} else {
		cmd = exec.Command(py, append([]string{"-m", "pip", "install", "--quiet", "--disable-pip-version-check", "--target", site}, pkgs...)...)
	}
	cmd.Env = append(os.Environ(), "PYTHONPATH=")
	cmd.Stdout, cmd.Stderr = c.Log, c.Log
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("installing Python instrumentation: %w", err)
	}
	return boot, site, os.WriteFile(ok, nil, 0o644)
}

const probe = "import sys; print(sys.executable); print('%d.%d' % sys.version_info[:2])"

func findPython(argv []string) (exe, ver string, err error) {
	var tries [][]string
	b := base(argv[0])
	switch {
	case strings.HasPrefix(b, "python") || b == "py":
		tries = append(tries, []string{argv[0]})
	case b == "uv" || b == "poetry" || b == "pipenv" || b == "pdm" || b == "hatch":
		tries = append(tries, []string{argv[0], "run", "python"})
	}
	if p, err := exec.LookPath(argv[0]); err == nil {
		dir := filepath.Dir(p)
		for _, n := range []string{"python", "python3", "python.exe"} {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				tries = append(tries, []string{filepath.Join(dir, n)})
			}
		}
		if s := shebang(p); s != "" {
			tries = append(tries, []string{s})
		}
	}
	for _, v := range []string{".venv", "venv"} {
		for _, rel := range []string{"Scripts/python.exe", "bin/python"} {
			if p := filepath.Join(v, rel); fileExists(p) {
				abs, _ := filepath.Abs(p)
				tries = append(tries, []string{abs})
			}
		}
	}
	tries = append(tries, []string{"python3"}, []string{"python"})
	for _, t := range tries {
		out, err := exec.Command(t[0], append(t[1:], "-c", probe)...).Output()
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r", "")), "\n")
		if len(lines) >= 2 {
			return strings.TrimSpace(lines[len(lines)-2]), strings.TrimSpace(lines[len(lines)-1]), nil
		}
	}
	return "", "", fmt.Errorf("could not find the Python interpreter for %q; pass --runtime none and use opentelemetry-instrument yourself", argv[0])
}

func shebang(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	line, _, _ := bytes.Cut(buf[:n], []byte("\n"))
	if !bytes.HasPrefix(line, []byte("#!")) {
		return ""
	}
	fields := strings.Fields(string(line[2:]))
	if len(fields) == 0 || strings.HasSuffix(fields[0], "/env") {
		return ""
	}
	return fields[0]
}

func stamp(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))[:10]
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
