package templates

// SuryRepo fills apt/zimam-sury.sources and apt/zimam-sury.pref (§8.2).
type SuryRepo struct {
	URL  string // php_repo_url, the source's URIs
	Host string // its host, which the pin names as the origin
}
