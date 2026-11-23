package rclone

import (
	"cmp"
	"fmt"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/templates"
)

// HostKeyAlgorithms are the values an SFTP destination's HostKeyAlgorithms
// may take; the template checks them again.
var HostKeyAlgorithms = templates.HostKeyAlgorithms

// Destination is one destination as rclone.conf holds it. Password and
// Password2 are the crypt password and salt already in rclone's obscured
// form: the agent keeps them nowhere else (R05-D3).
type Destination struct {
	ID                contract.DestinationID
	Type              contract.DestinationType
	S3                *contract.S3Settings
	SFTP              *contract.SFTPSettings
	HostKeyAlgorithms string // SFTP: the pinned key's; empty until a test pins one
	Password          string
	Password2         string
}

func (d Destination) check() error {
	if err := d.ID.Validate(); err != nil {
		return fmt.Errorf("id %w", err)
	}
	switch {
	case d.Type == contract.DestinationS3 && d.S3 != nil && d.SFTP == nil:
		if err := d.S3.Validate(); err != nil {
			return err
		}
		if d.HostKeyAlgorithms != "" {
			return fmt.Errorf("%s: an s3 destination has no host key", d.ID)
		}
	case d.Type == contract.DestinationSFTP && d.SFTP != nil && d.S3 == nil:
		f := contract.FieldErrors{}
		f.Check("host", d.SFTP.Host)
		f.Check("port", d.SFTP.Port)
		f.Check("user", d.SFTP.User)
		f.Check("path", d.SFTP.Path)
		if err := f.Err(); err != nil {
			return err
		}
		if d.HostKeyAlgorithms != "" && !slices.Contains(HostKeyAlgorithms, d.HostKeyAlgorithms) {
			return fmt.Errorf("%s: unknown host key algorithms %q", d.ID, d.HostKeyAlgorithms)
		}
	default:
		return fmt.Errorf("%s: needs exactly the settings of its type %q", d.ID, d.Type)
	}
	// An obscured value is base64url, so it can't break a line of the file.
	for _, v := range []string{d.Password, d.Password2} {
		if plain, err := Reveal(v); err != nil || plain == "" {
			return fmt.Errorf("%s: a crypt key isn't in rclone's obscured form", d.ID)
		}
	}
	return nil
}

// RenderConfig renders rclone.conf for every destination, sorted by id: a
// raw s3 or sftp remote "<id>-raw" and a crypt remote "<id>" over it
// (R05-D2).
func RenderConfig(p paths.Paths, dests []Destination) ([]byte, error) {
	data := templates.RcloneConf{}
	for _, d := range dests {
		if err := d.check(); err != nil {
			return nil, err
		}
		r := templates.RcloneRemote{ID: d.ID, S3: d.S3, SFTP: d.SFTP, HostKeyAlgorithms: d.HostKeyAlgorithms,
			Password: d.Password, Password2: d.Password2}
		if d.S3 != nil {
			r.Target = string(d.S3.Bucket)
			if d.S3.Prefix != "" {
				r.Target += "/" + string(d.S3.Prefix)
			}
		} else {
			r.Target = string(d.SFTP.Path)
			r.KeyFile, r.KnownHostsFile = p.BackupSFTPKey(d.ID), p.BackupKnownHosts(d.ID)
		}
		data.Remotes = append(data.Remotes, r)
	}
	slices.SortFunc(data.Remotes, func(a, b templates.RcloneRemote) int { return cmp.Compare(a.ID, b.ID) })
	for i := 1; i < len(data.Remotes); i++ {
		if data.Remotes[i].ID == data.Remotes[i-1].ID {
			return nil, fmt.Errorf("destination %s is listed twice", data.Remotes[i].ID)
		}
	}
	return templates.Render("backup/rclone.conf", data)
}
