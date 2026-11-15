package hardencmd

// configPath is the path a command reads: the flag when it was given, the component's default
// otherwise.
func configPath(env *Env, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return env.ConfigPath
}
