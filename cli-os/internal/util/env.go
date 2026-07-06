// env.go scrubs secrets out of the environment handed to a child process the engine or gateway
// execs on a model's behalf (docs/android-architecture.md §4 G8). A run_command/git_command tool
// call is effectively "let the model choose a command to run"; the vault master key and the
// first-run setup secret exist to be read ONLY by this process, so neither may ever appear in an
// exec.Cmd.Env built from os.Environ() for a model-directed or clone child process.
package util

// scrubbedSecretEnvVars are stripped from every child-process env this process launches on behalf
// of a model or an autonomous run. Add a new process-wide secret env var here, not at each call
// site, so nothing has to remember to scrub it individually.
var scrubbedSecretEnvVars = []string{"LOOPRITE_MASTER_KEY", "LOOPRITE_SETUP_SECRET"}

// ScrubSecretEnv returns a copy of env ("KEY=value" entries, as os.Environ() produces) with the
// vars above removed. A nil or empty env is returned unchanged, so a caller that always passes
// os.Environ() is unaffected when there is nothing to scrub.
func ScrubSecretEnv(env []string) []string {
	if len(env) == 0 {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !isScrubbedEnvEntry(kv) {
			out = append(out, kv)
		}
	}
	return out
}

// isScrubbedEnvEntry reports whether kv ("KEY=value") names one of scrubbedSecretEnvVars exactly
// (a "=" must follow the key) — "LOOPRITE_MASTER_KEYSTORE=x" is a different variable and must NOT
// be scrubbed just because it shares a prefix.
func isScrubbedEnvEntry(kv string) bool {
	for _, k := range scrubbedSecretEnvVars {
		if len(kv) > len(k) && kv[:len(k)] == k && kv[len(k)] == '=' {
			return true
		}
	}
	return false
}
