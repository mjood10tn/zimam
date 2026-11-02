package bootstrap

import "fmt"

// hint says what to check when a step fails. Every step can be retried by
// running the installer again: finished steps are skipped.
func hint(step string, o Options) string {
	switch step {
	case "agent-config":
		return "Check the installer's flags. A release that fails verification was changed in transit or has expired."
	case "directories":
		return "Zimam uses only directories that root owns and that aren't symlinks. Move the path named above out of the way, then run the installer again."
	case "sury-repository":
		return "This server must reach packages.sury.org over HTTPS. Check its DNS, outgoing firewall and proxy, then run the installer again."
	case "php":
		return "apt-get couldn't install PHP. Run `apt-get update` to see why, then run the installer again."
	case "nginx":
		return "Nginx rejected the configuration, so the previous files were put back. Run `nginx -t` to see why."
	case "panel-release":
		return "The panel release couldn't be downloaded, verified or unpacked. Check that this server can reach the release server, then run the installer again."
	case "panel-site":
		return "PHP-FPM or Nginx rejected the panel's configuration, so the previous files were put back. Run `nginx -t` to see why."
	case "panel-certificate":
		return fmt.Sprintf("DNS for %s doesn't point at this server, or port 80 isn't reachable from the internet. Fix that, then run the installer again.", o.PanelDomain)
	case "units":
		return "systemd rejected Zimam's units. Run `systemd-analyze verify /etc/systemd/system/zimam-*` to see why."
	case "log-policy":
		return "logrotate rejected the log rules. Run `logrotate -d /etc/zimam/logrotate.conf` to see why."
	case "setup-link":
		return "The panel couldn't issue a setup link. Run `sudo zimam-agent panel setup-link` to try again."
	}
	return ""
}
