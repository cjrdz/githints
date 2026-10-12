package cli

// cliVersion is injected by Main from the binary's build stamp.
var cliVersion string

func versionString() string {
	if cliVersion == "" {
		return "dev"
	}
	return cliVersion
}
