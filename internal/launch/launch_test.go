package launch

import "testing"

func TestDetect(t *testing.T) {
	cases := map[string]Runtime{
		"npm": Node, "node": Node, "/opt/tools/pnpm.cmd": Node, "/usr/bin/npx": Node, "tsx": Node,
		"python": Python, "python3.12": Python, "uvicorn": Python, "uv": Python, "app.py": Python, "flask": Python,
		"go": None, "cargo": None,
	}
	for cmd, want := range cases {
		if got := Detect([]string{cmd}); got != want {
			t.Errorf("Detect(%q) = %s, want %s", cmd, got, want)
		}
	}
	if Detect(nil) != None {
		t.Error("empty argv")
	}
}

func TestEnvNone(t *testing.T) {
	env, err := Env(Config{Runtime: None, Endpoint: "http://127.0.0.1:1", Service: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for _, kv := range env {
		m[kv] = true
	}
	if !m["OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:1"] || !m["OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf"] {
		t.Fatalf("env = %v", env)
	}
}
