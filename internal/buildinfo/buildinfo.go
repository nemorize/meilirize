package buildinfo

type Info struct {
	Version string
	Commit  string
	Date    string
}

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func Current() Info {
	return Info{
		Version: version,
		Commit:  commit,
		Date:    date,
	}
}
