package workflowyaml

import (
	"os/exec"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/templating"
)

const ghaExample = `name: CI
on:
  push:
    branches: [main]
  schedule:
    - cron: "0 3 * * *"
env:
  STAGE: ci
jobs:
  test:
    runs-on: ubuntu-latest
    needs: build
    timeout-minutes: 10
    env:
      TOKEN: ${{ secrets.API_TOKEN }}
    steps:
      - run: make test
  build:
    name: Build
    runs-on: ubuntu-22.04
    container: golang:1.25
    steps:
      - name: Compile
        run: go build ./...
  notify:
    runs-on: ubuntu-latest
    needs: [build, test]
    if: ${{ failure() }}
    steps:
      - run: echo failed
`

func TestParseGitHubActions(t *testing.T) {
	def, err := Parse([]byte(ghaExample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.Name != "CI" || def.Schedule != "0 3 * * *" || len(def.Tasks) != 3 {
		t.Fatalf("def = %q schedule %q with %d tasks", def.Name, def.Schedule, len(def.Tasks))
	}
	byID := map[string]models.TaskDefinition{}
	for _, task := range def.Tasks {
		byID[task.ID] = task
	}
	build, test, notify := byID["build"], byID["test"], byID["notify"]
	if build.Name != "Build" || build.Container.Image != "golang:1.25" || len(build.Dependencies) != 0 {
		t.Errorf("build = %+v", build)
	}
	if test.Container.Image != "ubuntu:24.04" || test.Timeout != 10*time.Minute || len(test.Dependencies) != 1 || test.Dependencies[0] != "build" {
		t.Errorf("test = %+v", test)
	}
	env, _ := test.Config["env"].(map[string]any)
	if env["STAGE"] != "ci" || env["TOKEN"] != `{{ print "" (secret "API_TOKEN") "" }}` {
		t.Errorf("test env = %v, want workflow env plus the secret as a template", env)
	}
	if notify.TriggerRule != models.TriggerRuleOneFailed || len(notify.Dependencies) != 2 {
		t.Errorf("notify = %+v, want one_failed after build and test", notify)
	}
}

func TestParseGitHubActions_Rejects(t *testing.T) {
	job := func(body string) string {
		return "jobs:\n  a:\n    runs-on: ubuntu-latest\n" + body
	}
	tests := []struct{ name, yaml, want string }{
		{"uses", job("    steps:\n      - uses: actions/checkout@v4\n"), "uses: actions/checkout@v4 is not supported"},
		{"matrix", job("    strategy:\n      matrix:\n        go: [1, 2]\n    steps:\n      - run: x\n"), "strategy"},
		{"services", job("    services:\n      db:\n        image: postgres\n    steps:\n      - run: x\n"), "services"},
		{"other expression", job("    steps:\n      - run: echo ${{ github.sha }}\n"), "${{ github.sha }} is not supported"},
		{"env expression in env", job("    env:\n      A: ${{ env.B }}\n    steps:\n      - run: x\n"), "only secrets.NAME is"},
		{"windows", "jobs:\n  a:\n    runs-on: windows-latest\n    steps:\n      - run: x\n", "runs-on windows-latest has no matching image"},
		{"job if", job("    if: github.ref == 'refs/heads/main'\n    steps:\n      - run: x\n"), "if:"},
		{"shell", job("    steps:\n      - run: print(1)\n        shell: python\n"), "shell: python is not supported"},
		{"no steps", job(""), "has no steps"},
		{"two crons", "on:\n  schedule:\n    - cron: '0 1 * * *'\n    - cron: '0 2 * * *'\n" + job("    steps:\n      - run: x\n"), "keep one cron"},
		{"negative timeout", job("    timeout-minutes: -1\n    steps:\n      - run: x\n"), "timeout-minutes -1 must be between"},
		{"overflowing timeout", job("    timeout-minutes: 1e300\n    steps:\n      - run: x\n"), "must be between"},
		{"step env name", job("    steps:\n      - run: x\n        env:\n          A-B: 1\n"), "not a shell variable name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The generated script, rendered as the worker renders config, must behave
// like the job: steps in order, step env and working-directory applied, the
// first failure stopping the job, and text (including "{{" and a secret
// full of shell syntax) passed through literally.
func TestGHAScript_RunsLikeTheJob(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	src := `jobs:
  a:
    runs-on: ubuntu-latest
    env:
      GREETING: hello
      BRACED: "x{${{ secrets.TRICKY }}}{{y"
    steps:
      - name: Stdin
        run: |
          read line || true
          echo "after-read line=[$line]"
          echo "braced=$BRACED"
      - name: First
        run: |
          echo "$GREETING {{ not a template }} 'quoted'"
          echo "step env=$LOCAL_VAR"
        env:
          LOCAL_VAR: "${{ env.GREETING }} $HOME-literal \"q\""
      - name: Secret
        run: echo "secret=${{ secrets.TRICKY }}"
      - name: Where
        working-directory: ` + dir + `
        run: pwd
      - name: Leak check
        run: echo "leaked=${LOCAL_VAR:-none}"
      - name: Fail
        run: exit 3
      - name: Never
        run: echo should-not-run
`
	def, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cfg := def.Tasks[0].Config
	const tricky = `x'; echo PWNED; '$(echo PWNED2)`
	rendered, err := templating.Render(cfg, nil, template.FuncMap{templating.SecretFunc: func(name string) (string, error) {
		if name != "TRICKY" {
			t.Errorf("secret %q requested", name)
		}
		return tricky, nil
	}})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	r := rendered.(map[string]any)
	cmd := exec.CommandContext(t.Context(), "sh", "-c", r["script"].(string))
	for k, v := range r["env"].(map[string]any) {
		cmd.Env = append(cmd.Env, k+"="+v.(string))
	}
	cmd.Env = append(cmd.Env, "PATH=/usr/bin:/bin", "HOME=/nonexistent")
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 3 {
		t.Fatalf("exit = %v, want 3 from the failing step\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{
		"== First\nhello {{ not a template }} 'quoted'\n",
		`step env=hello $HOME-literal "q"` + "\n",
		"secret=" + tricky + "\n",
		dir + "\n",
		"leaked=none\n",
		"after-read line=[]\n",
		"braced=x{" + tricky + "}{{y\n",
		"== Fail\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "should-not-run") || strings.Contains(got, "\nPWNED") {
		t.Errorf("output ran what it should not:\n%s", got)
	}
}

func TestHeredocDelimiterAvoidsBody(t *testing.T) {
	if d := heredocDelimiter("echo\nFLUXOR_GHA_STEP_1\n", 1); d == "FLUXOR_GHA_STEP_1" {
		t.Errorf("delimiter %q appears as a line of the body", d)
	}
}
