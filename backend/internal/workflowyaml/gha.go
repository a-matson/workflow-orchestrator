package workflowyaml

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// A GitHub Actions workflow becomes ordinary container tasks: one per job,
// running the job's run: steps as one shell script. Task containers have no
// network, so anything that must be fetched (uses: actions, services) is
// rejected rather than imported as a task that can never work.

type ghaWorkflow struct {
	Name        string            `yaml:"name"`
	RunName     string            `yaml:"run-name"`
	On          yaml.Node         `yaml:"on"`
	Env         map[string]string `yaml:"env"`
	Permissions any               `yaml:"permissions"`
	Concurrency any               `yaml:"concurrency"`
	Jobs        map[string]ghaJob `yaml:"jobs"`
}

type ghaJob struct {
	Name           string            `yaml:"name"`
	RunsOn         any               `yaml:"runs-on"`
	Needs          ghaList           `yaml:"needs"`
	If             string            `yaml:"if"`
	Env            map[string]string `yaml:"env"`
	TimeoutMinutes float64           `yaml:"timeout-minutes"`
	Container      ghaContainer      `yaml:"container"`
	Steps          []ghaStep         `yaml:"steps"`
	Permissions    any               `yaml:"permissions"`
	Concurrency    any               `yaml:"concurrency"`
}

type ghaContainer struct {
	Image string            `yaml:"image"`
	Env   map[string]string `yaml:"env"`
}

// UnmarshalYAML accepts the short form, container: alpine:3.22.
func (c *ghaContainer) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		c.Image = n.Value
		return nil
	}
	type plain ghaContainer
	return n.Decode((*plain)(c))
}

type ghaStep struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Shell            string            `yaml:"shell"`
	WorkingDirectory string            `yaml:"working-directory"`
	Env              map[string]string `yaml:"env"`
	// Decoded only to name it in the error.
	Uses string `yaml:"uses"`
}

// ghaList is a YAML string or list of strings, as needs: allows.
type ghaList []string

func (l *ghaList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = ghaList{n.Value}
		return nil
	}
	return n.Decode((*[]string)(l))
}

// isGitHubActions reports whether doc looks like a GitHub Actions workflow
// rather than a native one.
func isGitHubActions(doc map[string]any) bool {
	_, jobs := doc["jobs"]
	_, tasks := doc["tasks"]
	return jobs && !tasks
}

func parseGitHubActions(src []byte) (*models.WorkflowDefinition, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	// An unsupported key (strategy, services, ...) must fail the import, not
	// be dropped from a job that would then behave differently.
	dec.KnownFields(true)
	var wf ghaWorkflow
	if err := dec.Decode(&wf); err != nil {
		return nil, fmt.Errorf("github actions: %w", err)
	}
	def := &models.WorkflowDefinition{
		Name:        wf.Name,
		Description: "Imported from a GitHub Actions workflow",
		MaxParallel: 10,
	}
	if def.Name == "" {
		def.Name = "Imported GitHub Actions workflow"
	}
	cron, err := ghaSchedule(&wf.On)
	if err != nil {
		return nil, err
	}
	def.Schedule = cron

	ids := make([]string, 0, len(wf.Jobs))
	for id := range wf.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids) // map order is random; a stable task order keeps exports diffable
	for _, id := range ids {
		task, err := ghaTask(id, wf.Jobs[id], wf.Env)
		if err != nil {
			return nil, fmt.Errorf("github actions: job %q: %w", id, err)
		}
		def.Tasks = append(def.Tasks, task)
	}
	return def, nil
}

// ghaSchedule returns the one cron of on.schedule, if any. Other triggers
// (push, pull_request, ...) have no Fluxor equivalent and are ignored; a
// webhook or the API starts those runs instead.
func ghaSchedule(on *yaml.Node) (string, error) {
	if on.Kind != yaml.MappingNode {
		return "", nil
	}
	for i := 0; i+1 < len(on.Content); i += 2 {
		if on.Content[i].Value != "schedule" {
			continue
		}
		var crons []struct {
			Cron string `yaml:"cron"`
		}
		if err := on.Content[i+1].Decode(&crons); err != nil {
			return "", fmt.Errorf("github actions: on.schedule: %w", err)
		}
		if len(crons) > 1 {
			return "", errors.New("github actions: on.schedule: a workflow has one schedule; keep one cron")
		}
		if len(crons) == 1 {
			return crons[0].Cron, nil
		}
	}
	return "", nil
}

const maxTimeoutMinutes = 7 * 24 * 60

var ghaRunnerImages = map[string]string{
	"ubuntu-latest": "ubuntu:24.04",
	"ubuntu-24.04":  "ubuntu:24.04",
	"ubuntu-22.04":  "ubuntu:22.04",
}

func ghaTask(id string, job ghaJob, workflowEnv map[string]string) (models.TaskDefinition, error) {
	// Bounded before the conversion: an overflowing or negative value would
	// come out <= 0, which means no timeout at all.
	if !(job.TimeoutMinutes >= 0 && job.TimeoutMinutes <= maxTimeoutMinutes) {
		return models.TaskDefinition{}, fmt.Errorf("timeout-minutes %v must be between 0 and %d", job.TimeoutMinutes, maxTimeoutMinutes)
	}
	task := models.TaskDefinition{
		ID:           id,
		Name:         job.Name,
		Type:         "generic",
		Dependencies: []string(job.Needs),
		Timeout:      time.Duration(job.TimeoutMinutes * float64(time.Minute)),
	}
	if task.Name == "" {
		task.Name = id
	}
	if task.Dependencies == nil {
		task.Dependencies = []string{}
	}

	image := job.Container.Image
	if image == "" {
		runner, _ := job.RunsOn.(string)
		if image = ghaRunnerImages[runner]; image == "" {
			return task, fmt.Errorf("runs-on %v has no matching image; set container: to the image the job needs", job.RunsOn)
		}
	}
	task.Container = &models.ContainerSpec{Image: image}

	switch strings.TrimSpace(unwrapExpr(job.If)) {
	case "", "success()":
	case "always()":
		task.TriggerRule = models.TriggerRuleAllDone
	case "failure()":
		task.TriggerRule = models.TriggerRuleOneFailed
	default:
		return task, fmt.Errorf("if: %q is not supported; only success(), always() and failure() map to trigger rules", job.If)
	}

	env := map[string]any{}
	for _, layer := range []map[string]string{workflowEnv, job.Env, job.Container.Env} {
		for k, v := range layer {
			tv, err := ghaEnvValue(v)
			if err != nil {
				return task, fmt.Errorf("env %s: %w", k, err)
			}
			env[k] = tv
		}
	}
	script, err := ghaScript(job.Steps, env)
	if err != nil {
		return task, err
	}
	task.Config = map[string]any{"script": escapeTemplate(script)}
	if len(env) > 0 {
		task.Config["env"] = env
	}
	return task, nil
}

// ghaScript joins steps into one POSIX sh script. Each step is written to a
// file and run from it in its own subshell, so its env and cd do not leak,
// its stdin is empty rather than the rest of the script, and the script stops
// at the first failing step, as a job does. Expressions in it become
// references to env entries it adds to env.
func ghaScript(steps []ghaStep, env map[string]any) (string, error) {
	if len(steps) == 0 {
		return "", errors.New("has no steps")
	}
	var b strings.Builder
	b.WriteString("fluxor_gha_steps=$(mktemp -d) || exit 1\n")
	for i, st := range steps {
		label := st.Name
		if label == "" {
			label = fmt.Sprintf("step %d", i+1)
		}
		if st.Uses != "" {
			return "", fmt.Errorf("%s: uses: %s is not supported: tasks run without network access, so actions cannot be fetched; use run: steps", label, st.Uses)
		}
		if st.Run == "" {
			return "", fmt.Errorf("%s: has no run: script", label)
		}
		shell, err := ghaShell(st.Shell)
		if err != nil {
			return "", fmt.Errorf("%s: %w", label, err)
		}
		run, err := ghaShellRefs(st.Run, env, func(s string) string { return s })
		if err != nil {
			return "", fmt.Errorf("%s: %w", label, err)
		}

		// A quoted heredoc writes the step verbatim: no expansion by this
		// outer script, and no quoting of the step's own text to get wrong.
		file := fmt.Sprintf(`"$fluxor_gha_steps/%d"`, i+1)
		delim := heredocDelimiter(run, i+1)
		fmt.Fprintf(&b, "cat > %s <<'%s'\n%s\n%s\n", file, delim, strings.TrimSuffix(run, "\n"), delim)
		fmt.Fprintf(&b, "echo %s\n(\n", shellQuote("== "+label))
		keys := make([]string, 0, len(st.Env))
		for k := range st.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !envName.MatchString(k) {
				return "", fmt.Errorf("%s: env name %q is not a shell variable name", label, k)
			}
			v, err := ghaShellRefs(st.Env[k], env, escapeDoubleQuoted)
			if err != nil {
				return "", fmt.Errorf("%s: env %s: %w", label, k, err)
			}
			fmt.Fprintf(&b, "export %s=\"%s\"\n", k, v)
		}
		if st.WorkingDirectory != "" {
			fmt.Fprintf(&b, "cd -- %s\n", shellQuote(st.WorkingDirectory))
		}
		fmt.Fprintf(&b, "%s </dev/null\n) || exit $?\n", strings.ReplaceAll(shell, "FILE", file))
	}
	return b.String(), nil
}

// ghaShell is the command that runs a step's file, with FILE standing for
// its path, matching what GitHub runs for each shell: value.
func ghaShell(shell string) (string, error) {
	switch shell {
	case "":
		return "if command -v bash >/dev/null 2>&1; then bash -e FILE; else sh -e FILE; fi", nil
	case "bash":
		return "bash --noprofile --norc -eo pipefail FILE", nil
	case "sh":
		return "sh -e FILE", nil
	default:
		return "", fmt.Errorf("shell: %s is not supported; use bash or sh", shell)
	}
}

func heredocDelimiter(body string, n int) string {
	delim := fmt.Sprintf("FLUXOR_GHA_STEP_%d", n)
	for lines := "\n" + body + "\n"; strings.Contains(lines, "\n"+delim+"\n"); {
		delim += "_"
	}
	return delim
}

var (
	ghaExpr    = regexp.MustCompile(`\$\{\{\s*(.*?)\s*\}\}`)
	envName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretExpr = regexp.MustCompile(`^secrets\.([A-Za-z_][A-Za-z0-9_]*)$`)
	envExpr    = regexp.MustCompile(`^env\.([A-Za-z_][A-Za-z0-9_]*)$`)
)

func unwrapExpr(s string) string {
	if m := ghaExpr.FindStringSubmatch(strings.TrimSpace(s)); m != nil && m[0] == strings.TrimSpace(s) {
		return m[1]
	}
	return s
}

// ghaShellRefs rewrites the expressions in s, which shell code will read, as
// variable references: ${{ env.X }} as ${X}, and ${{ secrets.X }} as a
// variable whose env entry resolves the secret in the worker. A secret thus
// reaches the shell as a value, never as code it would parse. lit escapes
// the text between expressions.
func ghaShellRefs(s string, env map[string]any, lit func(string) string) (string, error) {
	var b strings.Builder
	last := 0
	for _, m := range ghaExpr.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(lit(s[last:m[0]]))
		expr := s[m[2]:m[3]]
		switch {
		case secretExpr.MatchString(expr):
			name := secretExpr.FindStringSubmatch(expr)[1]
			ref := "GHA_SECRET_" + name
			env[ref] = `{{ secret "` + name + `" }}`
			b.WriteString("${" + ref + "}")
		case envExpr.MatchString(expr):
			b.WriteString("${" + envExpr.FindStringSubmatch(expr)[1] + "}")
		default:
			return "", fmt.Errorf("expression ${{ %s }} is not supported; only secrets.NAME and env.NAME are", expr)
		}
		last = m[1]
	}
	b.WriteString(lit(s[last:]))
	return b.String(), nil
}

// ghaEnvValue converts a job or workflow env value, which is passed to the
// container as is, into a config template. A value with secrets becomes one
// print action over quoted literals, so no literal text can merge with the
// action's braces.
func ghaEnvValue(v string) (string, error) {
	matches := ghaExpr.FindAllStringSubmatchIndex(v, -1)
	if len(matches) == 0 {
		return escapeTemplate(v), nil
	}
	args := []string{}
	last := 0
	for _, m := range matches {
		args = append(args, strconv.Quote(v[last:m[0]]))
		expr := v[m[2]:m[3]]
		sm := secretExpr.FindStringSubmatch(expr)
		if sm == nil {
			return "", fmt.Errorf("expression ${{ %s }} is not supported here; only secrets.NAME is", expr)
		}
		args = append(args, `(secret "`+sm[1]+`")`)
		last = m[1]
	}
	args = append(args, strconv.Quote(v[last:]))
	return "{{ print " + strings.Join(args, " ") + " }}", nil
}

// escapeTemplate keeps text literal through config templating (F4), where
// "{{" would start an action.
func escapeTemplate(s string) string {
	return strings.ReplaceAll(s, "{{", `{{"{{"}}`)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func escapeDoubleQuoted(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s)
}
